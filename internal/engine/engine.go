// Package engine executes pipelines: it dispatches handoffs between standard
// agents, coordinates orchestrator stages with fan-in joins, pauses at human
// approval gates, and persists every state change.
package engine

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/kaladinstorm84/SignalBox/internal/knowledgebase"
	"github.com/kaladinstorm84/SignalBox/internal/pipeline"
	"github.com/kaladinstorm84/SignalBox/internal/runtime"
	"github.com/kaladinstorm84/SignalBox/internal/schema"
)

// Engine runs executions. One mutex guards all execution state; runtime
// invocations (the slow part) happen outside the lock.
type Engine struct {
	mu            sync.Mutex
	pipelines     map[string]*pipeline.Pipeline
	registry      *runtime.Registry
	store         *Store
	execs         map[string]*liveExec
	callSlots     chan struct{}
	projectRoot   string
	knowledge     *knowledgebase.Store
	launchWorker  func(WorkerRequest) (string, error)
	persistError  error
	workerStarted bool
	// uploadLimits bound new executions' files; filesRoot is the absolute
	// uploads folder, resolved on first use.
	uploadLimits UploadLimits
	filesRoot    string
}

// liveExec is the in-memory state wrapped around a persisted Execution.
type liveExec struct {
	exec           *Execution
	p              *pipeline.Pipeline
	ctx            context.Context
	cancel         context.CancelFunc
	liveRuns       int
	done           chan struct{}
	approvals      map[string]chan approvalDecision
	manualEdits    map[string]chan manualEditDecision
	retries        map[string]*pendingRetry
	orchs          map[string]*orchInstance
	outputsChanged chan struct{}
	reworks        map[string]*reworkState
	// joins holds fan-in nodes outside any stage still waiting for inputs.
	joins        map[string][]deliveredInput
	joinExpected map[string]int
	// entry overrides the pipeline entry for a child started part-way in;
	// stopAt names an agent whose handoffs are not followed.
	entry  string
	stopAt string
	// children maps a pipeline-node run ID to its live child execution.
	children map[string]*liveExec
}

func (le *liveExec) entryID() string {
	if le.entry != "" {
		return le.entry
	}
	return le.p.Entry
}

// pendingRetry is a run parked after a failed agent call.
type pendingRetry struct {
	run    *NodeRun
	resume Status
	ch     chan bool
}

type approvalDecision struct {
	approved   bool
	approvedBy string
	comment    string
}

type manualEditDecision struct {
	output   string
	editedBy string
}

// orchInstance is the singleton per-execution state of one orchestrator node.
type orchInstance struct {
	node        *pipeline.Node
	run         *NodeRun
	expected    int
	inputs      []deliveredInput
	inputsReady chan struct{}
	readyClosed bool
	cur         *stageState
	// startStage and resumeContext let a restarted orchestrator skip stages
	// that completed in the source execution.
	startStage    int
	resumeContext string
}

type deliveredInput struct {
	from  string
	input string
}

type reworkState struct {
	counts   map[string]int
	joins    map[string][]deliveredInput
	returnTo string
	gates    map[string]int
}

// stageState tracks the chains currently running on behalf of one
// orchestrator stage. outstanding counts live runs in the stage context; when
// it reaches zero every chain has handed back and the stage can proceed.
type stageState struct {
	sr          *StageRun
	scope       string
	outstanding int
	done        chan struct{}
	closed      bool
	joins       map[string][]deliveredInput
	expected    map[string]int
}

// New builds an engine. Persisted history is loaded on demand.
func New(pipelines map[string]*pipeline.Pipeline, registry *runtime.Registry, store *Store) (*Engine, error) {
	return NewWithLimit(pipelines, registry, store, 4)
}

// NewWithLimit builds an engine with a maximum number of concurrent runtime calls.
func NewWithLimit(pipelines map[string]*pipeline.Pipeline, registry *runtime.Registry, store *Store, maxConcurrentCalls int, projectRoot ...string) (*Engine, error) {
	if maxConcurrentCalls < 1 {
		return nil, fmt.Errorf("maximum concurrent calls must be at least 1")
	}
	eng := &Engine{
		pipelines:    pipelines,
		registry:     registry,
		store:        store,
		execs:        map[string]*liveExec{},
		callSlots:    make(chan struct{}, maxConcurrentCalls),
		uploadLimits: DefaultUploadLimits(),
	}
	if len(projectRoot) > 0 {
		eng.projectRoot = projectRoot[0]
		eng.knowledge = knowledgebase.New(projectRoot[0])
	}
	return eng, nil
}

// Pipelines returns the loaded pipelines.
func (eng *Engine) Pipelines() map[string]*pipeline.Pipeline {
	eng.mu.Lock()
	defer eng.mu.Unlock()

	pipelines := make(map[string]*pipeline.Pipeline, len(eng.pipelines))
	for name, p := range eng.pipelines {
		pipelines[name] = p
	}
	return pipelines
}

// SetPipelines replaces the definitions used by future executions.
func (eng *Engine) SetPipelines(pipelines map[string]*pipeline.Pipeline) {
	eng.mu.Lock()
	defer eng.mu.Unlock()
	eng.pipelines = pipelines
}

// Start begins a new execution of the named pipeline and returns its ID.
// Declared parameters take their defaults.
func (eng *Engine) Start(pipelineName, input string) (string, error) {
	return eng.StartWithParams(pipelineName, input, nil)
}

// StartWithParams begins a new execution with parameter values. Values are
// validated against the pipeline's declarations before anything runs.
func (eng *Engine) StartWithParams(pipelineName, input string, params map[string]string) (string, error) {
	return eng.StartWith(StartOptions{Pipeline: pipelineName, Input: input, Params: params})
}

// startSpec describes a new execution. from, until, and parent are set for
// child executions started by pipeline nodes. files are new uploads; reuse
// carries over another execution's files.
type startSpec struct {
	pipeline    string
	input       string
	params      map[string]string
	from, until string
	parent      *ParentLink
	files       []FileInput
	reuse       *reusedFiles
}

func (eng *Engine) startLocked(spec startSpec) (*liveExec, error) {
	template, ok := eng.pipelines[spec.pipeline]
	if !ok {
		return nil, fmt.Errorf("unknown pipeline %q", spec.pipeline)
	}
	sources, err := eng.collectFilesLocked(template, spec)
	if err != nil {
		return nil, err
	}
	params, err := bindFileParams(template, spec.params, sources)
	if err != nil {
		return nil, err
	}
	p, resolved, err := template.WithParams(params)
	if err != nil {
		return nil, err
	}
	if spec.from == "" {
		if err := validateInput(p, spec.input); err != nil {
			return nil, err
		}
	}
	for _, id := range []string{spec.from, spec.until} {
		if id != "" && p.Node(id) == nil {
			return nil, fmt.Errorf("pipeline %q has no agent %q", spec.pipeline, id)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	le := &liveExec{
		exec: &Execution{
			ID:         newExecID(),
			Pipeline:   spec.pipeline,
			Input:      spec.input,
			Params:     resolved,
			Status:     StatusRunning,
			Definition: fingerprint(p),
			Parent:     spec.parent,
			CreatedAt:  time.Now().UTC(),
		},
		p:              p,
		ctx:            ctx,
		cancel:         cancel,
		done:           make(chan struct{}),
		approvals:      map[string]chan approvalDecision{},
		retries:        map[string]*pendingRetry{},
		orchs:          map[string]*orchInstance{},
		outputsChanged: make(chan struct{}),
		joinExpected:   p.JoinCounts(pipeline.TopScope),
		entry:          spec.from,
		stopAt:         spec.until,
	}
	if spec.from != "" {
		le.joinExpected = joinCountsFrom(p, spec.from)
	}
	if spec.from != "" || spec.until != "" {
		le.exec.Section = &Section{From: spec.from, Until: spec.until}
	}
	files, err := eng.storeFilesLocked(le.exec.ID, sources)
	if err != nil {
		cancel()
		return nil, err
	}
	le.exec.Files = files

	eng.execs[le.exec.ID] = le
	eng.deliverLocked(le, le.entryID(), spec.input, "", nil)
	eng.persistLocked(le)
	return le, nil
}

// Approve resolves a pending approval gate.
func (eng *Engine) Approve(execID, runID string, approved bool, approvedBy, comment string) error {
	if _, remote, err := eng.remote(WorkerRequest{Op: "approve", ExecID: execID, RunID: runID, Decision: approved, Actor: approvedBy, Text: comment}); remote {
		return err
	}
	eng.mu.Lock()
	defer eng.mu.Unlock()
	le, ok := eng.execs[execID]
	if !ok {
		return fmt.Errorf("unknown execution %q", execID)
	}
	le = ownerLocked(le, func(x *liveExec) bool { _, ok := x.approvals[runID]; return ok })
	ch, ok := le.approvals[runID]
	if !ok {
		return fmt.Errorf("no pending approval %q in execution %q", runID, execID)
	}
	delete(le.approvals, runID)
	ch <- approvalDecision{approved: approved, approvedBy: approvedBy, comment: comment}
	return nil
}

// SubmitManualEdit resumes a manual_edit node with the edited payload.
func (eng *Engine) SubmitManualEdit(execID, runID, output, editedBy string) error {
	if _, remote, err := eng.remote(WorkerRequest{Op: "edit", ExecID: execID, RunID: runID, Text: output, Actor: editedBy}); remote {
		return err
	}
	eng.mu.Lock()
	defer eng.mu.Unlock()
	le, ok := eng.execs[execID]
	if !ok {
		return fmt.Errorf("unknown execution %q", execID)
	}
	le = ownerLocked(le, func(x *liveExec) bool { _, ok := x.manualEdits[runID]; return ok })
	ch, ok := le.manualEdits[runID]
	if !ok {
		return fmt.Errorf("no pending manual edit %q in execution %q", runID, execID)
	}
	if le.exec.Status.Terminal() {
		return fmt.Errorf("execution %q is already finished (%s)", le.exec.ID, le.exec.Status)
	}
	delete(le.manualEdits, runID)
	ch <- manualEditDecision{output: output, editedBy: editedBy}
	return nil
}

// ResolveFailure resumes a run paused after a failed agent call: retry
// re-invokes the agent with the same input; otherwise the execution fails.
func (eng *Engine) ResolveFailure(execID, runID string, retry bool) error {
	if _, remote, err := eng.remote(WorkerRequest{Op: "retry", ExecID: execID, RunID: runID, Decision: retry}); remote {
		return err
	}
	eng.mu.Lock()
	defer eng.mu.Unlock()
	le, ok := eng.execs[execID]
	if !ok {
		return fmt.Errorf("unknown execution %q", execID)
	}
	le = ownerLocked(le, func(x *liveExec) bool { _, ok := x.retries[runID]; return ok })
	pending, ok := le.retries[runID]
	if !ok {
		return fmt.Errorf("no failed run %q awaiting retry in execution %q", runID, execID)
	}
	delete(le.retries, runID)
	if retry {
		// Update state here, not in the waiting goroutine, so callers never
		// observe a stale waiting_retry status after a successful Retry.
		pending.run.Status = pending.resume
		pending.run.Error = ""
		pending.run.Retries++
		eng.persistLocked(le)
	}
	pending.ch <- retry
	return nil
}

// Retry re-runs a failed agent call that is awaiting retry.
func (eng *Engine) Retry(execID, runID string) error {
	return eng.ResolveFailure(execID, runID, true)
}

// Cancel aborts a running execution.
func (eng *Engine) Cancel(execID string) error {
	if _, remote, err := eng.remote(WorkerRequest{Op: "cancel", ExecID: execID}); remote {
		return err
	}
	eng.mu.Lock()
	defer eng.mu.Unlock()
	le, ok := eng.execs[execID]
	if !ok {
		return fmt.Errorf("unknown execution %q", execID)
	}
	if le.exec.Status.Terminal() {
		return fmt.Errorf("execution %q already finished (%s)", execID, le.exec.Status)
	}
	le.exec.Status = StatusCanceled
	le.exec.Error = "canceled by user"
	if le.cancel != nil {
		le.cancel()
	}
	eng.cancelChildrenLocked(le, fmt.Sprintf("parent execution %s was canceled", execID))
	eng.persistLocked(le)
	return nil
}

// Delete removes a finished execution from memory and disk. Executions that
// are still running (including canceled ones whose agent calls have not yet
// wound down) cannot be deleted.
func (eng *Engine) Delete(execID string) error {
	if _, remote, err := eng.remote(WorkerRequest{Op: "delete", ExecID: execID}); remote {
		return err
	}
	eng.mu.Lock()
	defer eng.mu.Unlock()
	le, ok := eng.execs[execID]
	if !ok {
		if eng.store == nil {
			return fmt.Errorf("unknown execution %q", execID)
		}
		execution, err := eng.store.Get(execID)
		if err != nil {
			return err
		}
		if !execution.Status.Terminal() {
			return fmt.Errorf("execution %q is still running (%s); cancel it first", execID, execution.Status)
		}
		if err := eng.store.Delete(execID); err != nil {
			return fmt.Errorf("deleting execution %q: %w", execID, err)
		}
		eng.removeUploadsLocked(execID)
		return nil
	}
	select {
	case <-le.done:
	default:
		if le.exec.Status.Terminal() {
			return fmt.Errorf("execution %q is still stopping; try again shortly", execID)
		}
		return fmt.Errorf("execution %q is still running (%s); cancel it first", execID, le.exec.Status)
	}
	if eng.store != nil {
		if err := eng.store.Delete(execID); err != nil {
			return fmt.Errorf("deleting execution %q: %w", execID, err)
		}
	}
	eng.removeUploadsLocked(execID)
	delete(eng.execs, execID)
	return nil
}

// Get returns a deep copy of an execution, safe to serialise concurrently.
func (eng *Engine) Get(execID string) (*Execution, error) {
	eng.mu.Lock()
	defer eng.mu.Unlock()
	le, ok := eng.execs[execID]
	if !ok {
		if eng.store == nil {
			return nil, fmt.Errorf("unknown execution %q", execID)
		}
		return eng.store.Get(execID)
	}
	data, err := json.Marshal(le.exec)
	if err != nil {
		return nil, err
	}
	var cp Execution
	if err := json.Unmarshal(data, &cp); err != nil {
		return nil, err
	}
	return &cp, nil
}

// List returns summaries of all executions, newest first.
func (eng *Engine) List() []Summary {
	eng.mu.Lock()
	defer eng.mu.Unlock()
	if eng.store != nil {
		items, _, err := eng.store.ListPage(1, int(^uint(0)>>1))
		if err == nil {
			return items
		}
		return nil
	}
	out := make([]Summary, 0, len(eng.execs))
	for _, le := range eng.execs {
		waiting, manualEdits, retrying := 0, 0, 0
		for _, r := range le.exec.Runs {
			switch r.Status {
			case StatusWaitingApproval:
				waiting++
			case StatusWaitingManualEdit:
				manualEdits++
			case StatusWaitingRetry:
				retrying++
			}
		}
		out = append(out, Summary{
			ID:          le.exec.ID,
			Pipeline:    le.exec.Pipeline,
			Input:       firstChars(le.exec.Input, 160),
			Status:      le.exec.Status,
			Waiting:     waiting,
			ManualEdits: manualEdits,
			Retrying:    retrying,
			CreatedAt:   le.exec.CreatedAt,
			FinishedAt:  le.exec.FinishedAt,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out
}

// ListPage returns one newest-first execution page and the total history size.
func (eng *Engine) ListPage(page, pageSize int) ([]Summary, int, error) {
	if page < 1 || pageSize < 1 {
		return nil, 0, fmt.Errorf("page and page size must be positive")
	}
	eng.mu.Lock()
	defer eng.mu.Unlock()
	if eng.store != nil {
		return eng.store.ListPage(page, pageSize)
	}
	out := make([]Summary, 0, len(eng.execs))
	for _, le := range eng.execs {
		out = append(out, summarizeExecution(le.exec))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	total := len(out)
	start := (page - 1) * pageSize
	if start >= total {
		return []Summary{}, total, nil
	}
	end := start + pageSize
	if end > total {
		end = total
	}
	return out[start:end], total, nil
}

// WaitingApprovals returns the runs currently blocked on human approval.
func (eng *Engine) WaitingApprovals(execID string) []*NodeRun {
	return eng.runsWithStatus(execID, StatusWaitingApproval)
}

// WaitingManualEdits returns the runs currently waiting for a manual edit.
func (eng *Engine) WaitingManualEdits(execID string) []*NodeRun {
	return eng.runsWithStatus(execID, StatusWaitingManualEdit)
}

// WaitingRetries returns the runs paused after a failed agent call.
func (eng *Engine) WaitingRetries(execID string) []*NodeRun {
	return eng.runsWithStatus(execID, StatusWaitingRetry)
}

func (eng *Engine) runsWithStatus(execID string, status Status) []*NodeRun {
	eng.mu.Lock()
	defer eng.mu.Unlock()
	le, ok := eng.execs[execID]
	if !ok {
		var out []*NodeRun
		for _, execution := range eng.persistedTree(execID, true) {
			for _, run := range execution.Runs {
				if run.Status == status {
					out = append(out, run)
				}
			}
		}
		return out
	}
	var out []*NodeRun
	var collect func(x *liveExec)
	collect = func(x *liveExec) {
		for _, r := range x.exec.Runs {
			if r.Status == status {
				cp := *r
				out = append(out, &cp)
			}
		}
		// Runs waiting in child executions are included so they can be
		// handled through the parent (see ownerLocked).
		for _, child := range sortedChildren(x) {
			collect(child)
		}
	}
	collect(le)
	return out
}

// SurfacedRuns lists runs waiting for a person (approval, manual edit, or
// retry) in the live descendants of an execution.
func (eng *Engine) SurfacedRuns(execID string) []SurfacedRun {
	eng.mu.Lock()
	defer eng.mu.Unlock()
	le, ok := eng.execs[execID]
	if !ok {
		var out []SurfacedRun
		for _, execution := range eng.persistedTree(execID, false) {
			for _, run := range execution.Runs {
				switch run.Status {
				case StatusWaitingApproval, StatusWaitingManualEdit, StatusWaitingRetry:
					run.Snapshot = nil
					out = append(out, SurfacedRun{Execution: execution.ID, Pipeline: execution.Pipeline, ParentRun: execution.Parent.RunID, Run: run})
				}
			}
		}
		return out
	}
	var out []SurfacedRun
	var collect func(x *liveExec)
	collect = func(x *liveExec) {
		for _, child := range sortedChildren(x) {
			for _, r := range child.exec.Runs {
				switch r.Status {
				case StatusWaitingApproval, StatusWaitingManualEdit, StatusWaitingRetry:
					cp := *r
					cp.Snapshot = nil
					out = append(out, SurfacedRun{Execution: child.exec.ID, Pipeline: child.exec.Pipeline, ParentRun: child.exec.Parent.RunID, Run: &cp})
				}
			}
			collect(child)
		}
	}
	collect(le)
	return out
}

// ownerLocked returns the execution, among le and its live descendants,
// for which has reports true; le itself when none match.
func ownerLocked(le *liveExec, has func(*liveExec) bool) *liveExec {
	var find func(x *liveExec) *liveExec
	find = func(x *liveExec) *liveExec {
		if has(x) {
			return x
		}
		for _, child := range x.children {
			if owner := find(child); owner != nil {
				return owner
			}
		}
		return nil
	}
	if owner := find(le); owner != nil {
		return owner
	}
	return le
}

func sortedChildren(le *liveExec) []*liveExec {
	children := make([]*liveExec, 0, len(le.children))
	for _, child := range le.children {
		children = append(children, child)
	}
	sort.Slice(children, func(i, j int) bool { return children[i].exec.ID < children[j].exec.ID })
	return children
}

// Wait returns a channel closed when the execution finishes.
func (eng *Engine) Wait(execID string) (<-chan struct{}, error) {
	eng.mu.Lock()
	defer eng.mu.Unlock()
	le, ok := eng.execs[execID]
	if !ok {
		if eng.store == nil {
			return nil, fmt.Errorf("unknown execution %q", execID)
		}
		execution, err := eng.store.Get(execID)
		if err != nil {
			return nil, err
		}
		if !execution.Status.Terminal() {
			return nil, fmt.Errorf("execution %q is not active in this process", execID)
		}
		done := make(chan struct{})
		close(done)
		return done, nil
	}
	return le.done, nil
}

// --- dispatch ---

// deliverLocked routes a handoff payload to a node. Caller holds eng.mu.
// octx is non-nil when the delivery happens inside an orchestrator stage.
func (eng *Engine) deliverLocked(le *liveExec, nodeID, input, from string, octx *stageState, rework ...*reworkState) {
	if le.exec.Status.Terminal() {
		return
	}
	node := le.p.Node(nodeID)
	var work *reworkState
	if len(rework) > 0 {
		work = rework[0]
	}

	if node.Type == pipeline.TypeOrchestrator {
		oi := le.orchs[nodeID]
		if oi == nil {
			expected := le.joinExpected[nodeID]
			if nodeID == le.entryID() {
				expected = 1
			}
			oi = &orchInstance{
				node:        node,
				expected:    expected,
				inputsReady: make(chan struct{}),
				run: &NodeRun{
					ID:         newRunID(),
					NodeID:     nodeID,
					Type:       node.Type,
					Format:     node.Format,
					Status:     StatusWaitingInputs,
					FromNodeID: from,
					Snapshot:   snapshotLocked(le.exec),
				},
			}
			le.orchs[nodeID] = oi
			le.exec.Runs = append(le.exec.Runs, oi.run)
			le.liveRuns++
			go eng.runOrchestrator(le, oi)
		}
		oi.inputs = append(oi.inputs, deliveredInput{from: from, input: input})
		if len(oi.inputs) >= oi.expected && !oi.readyClosed {
			oi.readyClosed = true
			close(oi.inputsReady)
		}
		return
	}

	// A node with several inbound deliveries in this scope waits for all of
	// them, then runs once on the combined input. A source is always still
	// live while its delivery is outstanding, so the scope cannot settle
	// before the join completes.
	scope, joins, expectedCounts := pipeline.TopScope, &le.joins, &le.joinExpected
	if octx != nil {
		scope, joins, expectedCounts = octx.scope, &octx.joins, &octx.expected
	}
	expected := 0
	if expectedCounts != nil {
		expected = (*expectedCounts)[nodeID]
	} else {
		expected = le.p.JoinCount(scope, nodeID)
	}
	if work != nil {
		expected = work.counts[nodeID]
		joins, expectedCounts = &work.joins, &work.counts
	}
	if expected > 1 {
		if *joins == nil {
			*joins = map[string][]deliveredInput{}
		}
		inputs := append((*joins)[nodeID], deliveredInput{from: from, input: input})
		if len(inputs) < expected {
			(*joins)[nodeID] = inputs
			return
		}
		delete(*joins, nodeID)
		input = joinInputs(inputs)
		froms := make([]string, len(inputs))
		for i, in := range inputs {
			froms[i] = in.from
		}
		from = strings.Join(froms, ", ")
	}
	eng.dispatchNodeLocked(le, node, input, from, octx, work)
}

func (eng *Engine) dispatchNodeLocked(le *liveExec, node *pipeline.Node, input, from string, octx *stageState, rework ...*reworkState) {
	run := &NodeRun{
		ID:         newRunID(),
		NodeID:     node.ID,
		Type:       node.Type,
		Format:     node.Format,
		Status:     StatusPending,
		Input:      input,
		FromNodeID: from,
		Snapshot:   snapshotLocked(le.exec),
	}
	if octx != nil {
		run.StageID = octx.sr.StageID
		run.OrchRunID = octx.sr.ownerRunID
		octx.outstanding++
	}
	if len(rework) > 0 && rework[0] != nil {
		if le.reworks == nil {
			le.reworks = map[string]*reworkState{}
		}
		le.reworks[run.ID] = rework[0]
	}
	le.exec.Runs = append(le.exec.Runs, run)
	le.liveRuns++
	go eng.runNode(le, run, node, octx)
}

// --- node execution ---

func (eng *Engine) runNode(le *liveExec, run *NodeRun, node *pipeline.Node, octx *stageState) {
	if !run.AdditionalInputResolved {
		if _, err := eng.awaitAdditionalInputs(le, run, node, run.Input); err != nil {
			eng.finishRunErr(le, run, node, octx, err)
			return
		}
	}
	if node.Type == pipeline.TypeApproval {
		eng.runApproval(le, run, node, octx)
		return
	}
	if node.Type == pipeline.TypeManualEdit {
		eng.runManualEdit(le, run, node, octx)
		return
	}

	eng.mu.Lock()
	now := time.Now().UTC()
	run.Status = StatusRunning
	run.StartedAt = &now
	run.Schema = node.OutputSchema()
	run.SchemaAttempts = nil
	run.Verdict = ""
	if run.Context == nil && node.ContextStrategy() != pipeline.ContextNone {
		run.Context = selectContext(node.Context, le.exec.Runs, run)
	}
	grantErr := eng.recordIntegrationsLocked(le, run, node)
	eng.persistLocked(le)
	eng.mu.Unlock()
	if grantErr != nil {
		eng.finishRunErr(le, run, node, octx, grantErr)
		return
	}

	if node.Type == pipeline.TypeLLMGate {
		eng.runLLMGate(le, run, node, octx)
		return
	}
	if node.Type == pipeline.TypeSession {
		eng.runSession(le, run, node, octx)
		return
	}
	if node.Type == pipeline.TypePipeline {
		eng.runPipelineNode(le, run, node, octx)
		return
	}

	delivery, err := eng.deliverFiles(le, run, node)
	if err != nil {
		eng.finishRunErr(le, run, node, octx, err)
		return
	}
	var invokeWith func(input string) (string, error)
	sch := le.p.SchemaFor(node.OutputSchema())
	if node.Type == pipeline.TypeCode {
		rt, err := runtime.NewCode(node, eng.projectRoot)
		if err != nil {
			eng.finishRunErr(le, run, node, octx, err)
			return
		}
		invokeWith = func(input string) (string, error) {
			req := runtime.Request{Input: input}
			delivery.apply(&req)
			return rt.Invoke(le.ctx, req)
		}
	} else {
		rt, err := eng.registry.Get(node.Runtime)
		if err != nil {
			eng.finishRunErr(le, run, node, octx, err)
			return
		}
		invokeWith = func(input string) (string, error) {
			knowledgeInput, err := eng.withKnowledge(input, node.KnowledgeBases)
			if err != nil {
				return "", err
			}
			req := agentRequest(run, node.Prompt, knowledgeInput)
			req.Schema = sch
			delivery.apply(&req)
			return rt.Invoke(le.ctx, req)
		}
	}
	retrying := func(input string) (string, error) {
		return eng.invokeRetrying(le, run, node.Retry, func() (string, error) { return invokeWith(input) })
	}
	fail := func(out string, err error) {
		var outcome *runtime.CodeOutcomeError
		if errors.As(err, &outcome) && outcome.Outcome == "retry" {
			eng.finishRunRejected(le, run, node, octx, outcome.Feedback, out)
			return
		}
		eng.finishRunErr(le, run, node, octx, err)
	}
	out, err := retrying(runtimeInput(run))
	if err != nil {
		fail(out, err)
		return
	}
	if sch != nil {
		if out, err = eng.conform(le, run, node, sch, out, schemaCheck(sch), retrying); err != nil {
			fail(out, err)
			return
		}
	}
	eng.finishRunOK(le, run, node, out, octx, "")
}

func (eng *Engine) runApproval(le *liveExec, run *NodeRun, node *pipeline.Node, octx *stageState) {
	eng.mu.Lock()
	now := time.Now().UTC()
	run.Status = StatusWaitingApproval
	run.StartedAt = &now
	ch := make(chan approvalDecision, 1)
	le.approvals[run.ID] = ch
	eng.persistLocked(le)
	eng.mu.Unlock()

	var decision approvalDecision
	select {
	case decision = <-ch:
	case <-le.ctx.Done():
		eng.mu.Lock()
		delete(le.approvals, run.ID)
		if !run.Status.Terminal() {
			run.Status = StatusCanceled
			finished := time.Now().UTC()
			run.FinishedAt = &finished
		}
		eng.releaseStageLocked(octx)
		eng.endRunLocked(le)
		eng.persistLocked(le)
		eng.mu.Unlock()
		return
	}

	eng.mu.Lock()
	run.ApprovedBy = decision.approvedBy
	run.Comment = decision.comment
	run.DecisionSource = "human"
	if decision.approved {
		run.Decision = "approve"
	} else {
		run.Decision = "reject"
	}
	eng.mu.Unlock()

	if decision.approved {
		// An approval gate passes its payload through unchanged.
		eng.finishRunOK(le, run, node, run.Input, octx, "approve")
		return
	}
	eng.finishRunRejected(le, run, node, octx, decision.comment, run.Input)
}

type gateVerdict struct {
	Decision *string `json:"decision"`
	Feedback *string `json:"feedback"`
}

func (eng *Engine) runLLMGate(le *liveExec, run *NodeRun, node *pipeline.Node, octx *stageState) {
	rt, err := eng.registry.Get(node.Runtime)
	if err != nil {
		eng.finishRunErr(le, run, node, octx, err)
		return
	}
	sch := le.p.SchemaFor(node.OutputSchema())
	delivery, err := eng.deliverFiles(le, run, node)
	if err != nil {
		eng.finishRunErr(le, run, node, octx, err)
		return
	}
	prompt := node.Prompt + "\n\nReturn only one JSON object with exactly two string fields, decision and feedback. " +
		`decision must be "approve" or "reject". Example: {"decision":"approve","feedback":"..."}.`
	if sch != nil {
		prompt = node.Prompt + "\n\nReturn only one JSON object conforming to the schema below. It must include the string fields decision and feedback; " +
			`decision must be "approve" or "reject".`
	}
	retrying := func(input string) (string, error) {
		return eng.invokeRetrying(le, run, node.Retry, func() (string, error) {
			knowledgeInput, err := eng.withKnowledge(input, node.KnowledgeBases)
			if err != nil {
				return "", err
			}
			req := agentRequest(run, prompt, knowledgeInput)
			req.Schema = sch
			delivery.apply(&req)
			return rt.Invoke(le.ctx, req)
		})
	}
	verdictText, err := retrying(runtimeInput(run))
	if err != nil {
		eng.finishRunErr(le, run, node, octx, err)
		return
	}
	var verdict gateVerdict
	if sch != nil {
		check := func(text string) (string, []string) {
			res := sch.Check(text)
			if len(res.Errors) > 0 {
				return res.JSON, res.Errors
			}
			return res.JSON, verdictErrors(res.Value)
		}
		normalized, err := eng.conform(le, run, node, sch, verdictText, check, retrying)
		if err != nil {
			eng.finishRunErr(le, run, node, octx, err)
			return
		}
		if err := json.Unmarshal([]byte(normalized), &verdict); err != nil {
			eng.finishRunErr(le, run, node, octx, fmt.Errorf("llm_gate %q returned invalid JSON verdict: %w", node.ID, err))
			return
		}
		eng.mu.Lock()
		run.Verdict = normalized
		eng.mu.Unlock()
	} else if verdict, err = parseStrictVerdict(node, verdictText); err != nil {
		eng.finishRunErr(le, run, node, octx, err)
		return
	}

	eng.mu.Lock()
	run.DecisionSource = "llm_gate"
	run.Decision = *verdict.Decision
	run.Comment = *verdict.Feedback
	if work := le.reworks[run.ID]; work != nil {
		run.GateReworks = work.gates[node.ID]
	}
	eng.persistLocked(le)
	eng.mu.Unlock()
	if *verdict.Decision == "approve" {
		eng.finishRunOK(le, run, node, run.Input, octx, "approve")
		return
	}
	eng.finishRunRejected(le, run, node, octx, *verdict.Feedback, run.Input)
}

// parseStrictVerdict parses a gate verdict without a schema: exactly one
// JSON object with only decision and feedback.
func parseStrictVerdict(node *pipeline.Node, verdictText string) (gateVerdict, error) {
	var verdict gateVerdict
	decoder := json.NewDecoder(strings.NewReader(verdictText))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&verdict); err != nil {
		return verdict, fmt.Errorf("llm_gate %q returned invalid JSON verdict: %w", node.ID, err)
	}
	if err := decoder.Decode(new(any)); err == nil {
		return verdict, fmt.Errorf("llm_gate %q returned more than one JSON value", node.ID)
	} else if !errors.Is(err, io.EOF) {
		return verdict, fmt.Errorf("llm_gate %q returned invalid trailing data: %w", node.ID, err)
	}
	if verdict.Decision == nil || verdict.Feedback == nil {
		return verdict, fmt.Errorf("llm_gate %q verdict must include decision and feedback", node.ID)
	}
	if *verdict.Decision != "approve" && *verdict.Decision != "reject" {
		return verdict, fmt.Errorf("llm_gate %q returned decision %q; want approve or reject", node.ID, *verdict.Decision)
	}
	return verdict, nil
}

func (eng *Engine) runManualEdit(le *liveExec, run *NodeRun, node *pipeline.Node, octx *stageState) {
	eng.mu.Lock()
	now := time.Now().UTC()
	run.Status = StatusWaitingManualEdit
	run.StartedAt = &now
	if le.manualEdits == nil {
		le.manualEdits = map[string]chan manualEditDecision{}
	}
	ch := make(chan manualEditDecision, 1)
	le.manualEdits[run.ID] = ch
	eng.persistLocked(le)
	eng.mu.Unlock()

	select {
	case decision := <-ch:
		eng.mu.Lock()
		run.EditedBy = decision.editedBy
		eng.mu.Unlock()
		eng.finishRunOK(le, run, node, decision.output, octx, "")
	case <-le.ctx.Done():
		eng.mu.Lock()
		delete(le.manualEdits, run.ID)
		if !run.Status.Terminal() {
			run.Status = StatusCanceled
			finished := time.Now().UTC()
			run.FinishedAt = &finished
		}
		eng.releaseStageLocked(octx)
		eng.endRunLocked(le)
		eng.persistLocked(le)
		eng.mu.Unlock()
	}
}

func (eng *Engine) finishRunRejected(le *liveExec, run *NodeRun, node *pipeline.Node, octx *stageState, note, output string) {
	eng.mu.Lock()
	finished := time.Now().UTC()
	run.Status = StatusRejected
	run.FinishedAt = &finished
	run.Comment = note
	run.Output = output
	if node.Type == pipeline.TypeCode || node.Type == pipeline.TypePipeline {
		run.Error = note
	}
	work := le.reworks[run.ID]
	reworked := false
	limitReached := false
	directRejectRoute := node.Type == pipeline.TypeApproval && node.RejectTo == "" && hasExplicitRejectRoute(node)
	if !le.exec.Status.Terminal() && node.Type == pipeline.TypeLLMGate && node.RejectTo != "" {
		reworks := 0
		if work != nil {
			reworks = work.gates[node.ID]
		}
		if reworks >= node.MaxReworks {
			limitReached = true
		} else {
			if work == nil {
				work = &reworkState{gates: map[string]int{}}
			} else if work.gates == nil {
				work.gates = map[string]int{}
			}
			work.gates[node.ID] = reworks + 1
			reworked = eng.reworkAfterRejectionLocked(le, run, node, octx, note, work)
			if !reworked {
				work.gates[node.ID] = reworks
			}
		}
	} else if !le.exec.Status.Terminal() && node.Type != pipeline.TypeLLMGate && !directRejectRoute {
		reworked = eng.reworkAfterRejectionLocked(le, run, node, octx, note, work)
	}
	if !reworked && !limitReached && !le.exec.Status.Terminal() {
		selected := selectHandoffs(node, "reject", nil)
		if hasExplicitRejectRoute(node) && len(selected) > 0 {
			eng.deactivateHandoffsLocked(le, node, selected, octx, work)
			eng.routeRunLocked(le, run, node, selected, output, octx, work)
			eng.releaseStageLocked(octx)
			eng.endRunLocked(le)
			eng.persistLocked(le)
			eng.mu.Unlock()
			return
		}
	}
	delete(le.reworks, run.ID)
	if reworked {
		eng.releaseStageLocked(octx)
		eng.endRunLocked(le)
		eng.persistLocked(le)
		eng.mu.Unlock()
		return
	}
	if !le.exec.Status.Terminal() {
		le.exec.Status = StatusRejected
		if limitReached {
			le.exec.Error = fmt.Sprintf("llm_gate %q rejected the result after reaching its rework limit (%d)", node.ID, node.MaxReworks)
		} else {
			le.exec.Error = fmt.Sprintf("node %q was rejected", node.ID)
		}
		if le.cancel != nil {
			le.cancel()
		}
	}
	eng.releaseStageLocked(octx)
	eng.endRunLocked(le)
	eng.persistLocked(le)
	eng.mu.Unlock()
}

func (eng *Engine) reworkAfterRejectionLocked(le *liveExec, rejected *NodeRun, node *pipeline.Node, octx *stageState, note string, inherited ...*reworkState) bool {
	runs := le.exec.Runs
	inheritedWork := le.reworks[rejected.ID]
	if len(inherited) > 0 {
		inheritedWork = inherited[0]
	}
	rejectedIndex := len(runs)
	for i, run := range runs {
		if run.ID == rejected.ID {
			rejectedIndex = i
			break
		}
	}
	if node.RejectTo != "" {
		input, from := rejected.Input, node.ID
		scope := pipeline.TopScope
		if octx != nil {
			scope = octx.scope
		}
		if le.p.JoinCount(scope, node.RejectTo) > 0 {
			found := false
			for i := rejectedIndex - 1; i >= 0; i-- {
				previous := runs[i]
				if previous.NodeID != node.RejectTo || previous.Status != StatusCompleted || previous.StageID != rejected.StageID || previous.OrchRunID != rejected.OrchRunID {
					continue
				}
				input, from = previous.Input, previous.FromNodeID
				found = true
				break
			}
			if !found {
				return false
			}
		}
		gates := map[string]int{}
		if inheritedWork != nil {
			for gateID, count := range inheritedWork.gates {
				gates[gateID] = count
			}
		}
		work := &reworkState{counts: joinCountsFrom(le.p, node.RejectTo), returnTo: node.ID, gates: gates}
		eng.dispatchNodeLocked(le, le.p.Node(node.RejectTo), appendRejectionNote(input, node.ID, note), from, octx, work)
		return true
	}

	for i := rejectedIndex - 1; i >= 0; i-- {
		previous := runs[i]
		if previous.Type != pipeline.TypeApproval || previous.Status != StatusCompleted || previous.StageID != rejected.StageID || previous.OrchRunID != rejected.OrchRunID {
			continue
		}
		checkpoint := le.p.Node(previous.NodeID)
		if checkpoint == nil || len(checkpoint.Handoff) == 0 || !handoffReaches(le.p, checkpoint.ID, node.ID) {
			continue
		}
		input := appendRejectionNote(previous.Output, node.ID, note)
		for _, target := range checkpoint.HandoffTargets() {
			eng.deliverLocked(le, target, input, checkpoint.ID, octx, inheritedWork)
		}
		return true
	}

	upstreamIDs := strings.Split(rejected.FromNodeID, ",")
	dispatched := false
	for _, upstreamID := range upstreamIDs {
		upstreamID = strings.TrimSpace(upstreamID)
		for i := rejectedIndex - 1; i >= 0; i-- {
			previous := runs[i]
			if previous.NodeID != upstreamID || previous.Status != StatusCompleted || previous.StageID != rejected.StageID || previous.OrchRunID != rejected.OrchRunID {
				continue
			}
			eng.deliverLocked(le, previous.NodeID, appendRejectionNote(previous.Input, node.ID, note), previous.FromNodeID, octx, inheritedWork)
			dispatched = true
			break
		}
	}
	return dispatched
}

func appendRejectionNote(input, approvalID, note string) string {
	note = strings.TrimSpace(note)
	if note == "" {
		return input
	}
	if input != "" {
		input += "\n\n"
	}
	return input + fmt.Sprintf("--- rejection notes from %s ---\n%s", approvalID, note)
}

func handoffReaches(p *pipeline.Pipeline, source, target string) bool {
	seen := map[string]bool{}
	queue := []string{source}
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		if id == target {
			return true
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		queue = append(queue, p.Node(id).HandoffTargets()...)
	}
	return false
}

// finishRunOK records success, dispatches handoffs (or hands back to the
// owning orchestrator stage / records a final output), and settles counters.
func (eng *Engine) finishRunOK(le *liveExec, run *NodeRun, node *pipeline.Node, output string, octx *stageState, outcome string) {
	eng.mu.Lock()
	defer eng.mu.Unlock()
	work := le.reworks[run.ID]
	delete(le.reworks, run.ID)
	if work != nil && work.returnTo == run.NodeID {
		work = nil
	}
	selected := selectHandoffs(node, outcome, structuredDoc(run, output))
	if run.NodeID == le.stopAt && octx == nil {
		// The end of a chained section: nothing after it runs, and its
		// output is a final output of the child execution.
		selected = nil
	}
	eng.deactivateHandoffsLocked(le, node, selected, octx, work)
	now := time.Now().UTC()
	run.Output = output
	run.Error = ""
	run.Status = StatusCompleted
	run.FinishedAt = &now
	close(le.outputsChanged)
	le.outputsChanged = make(chan struct{})

	if !le.exec.Status.Terminal() {
		if len(selected) > 0 {
			eng.routeRunLocked(le, run, node, selected, output, octx, work)
		} else if octx != nil {
			octx.sr.Outputs = append(octx.sr.Outputs, StageOutput{
				NodeRunID: run.ID,
				NodeID:    run.NodeID,
				Output:    output,
			})
		} else {
			le.exec.FinalOutputs = append(le.exec.FinalOutputs, FinalOutput{
				NodeRunID: run.ID,
				NodeID:    run.NodeID,
				Output:    output,
			})
		}
	}
	eng.releaseStageLocked(octx)
	eng.endRunLocked(le)
	eng.persistLocked(le)
}

func selectHandoffs(node *pipeline.Node, outcome string, doc any) []pipeline.HandoffRule {
	var selected []pipeline.HandoffRule
	for _, route := range node.Handoff {
		switch route.Condition {
		case "":
			if outcome != "reject" && outcome != OutcomeNoConsensus {
				selected = append(selected, route)
			}
		case "always":
			selected = append(selected, route)
		case "on_consensus":
			if outcome == OutcomeConsensus {
				selected = append(selected, route)
			}
		case "on_no_consensus":
			if outcome == OutcomeNoConsensus {
				selected = append(selected, route)
			}
		case "on_approve":
			if outcome == "approve" {
				selected = append(selected, route)
			}
		case "on_reject":
			if outcome == "reject" {
				selected = append(selected, route)
			}
		default:
			// A field comparison routes like an unconditional handoff when
			// it holds against the structured output.
			if outcome == "reject" || outcome == OutcomeNoConsensus {
				continue
			}
			if cond, err := schema.ParseCondition(route.Condition); err == nil && cond.Eval(doc) {
				selected = append(selected, route)
			}
		}
	}
	return selected
}

func hasExplicitRejectRoute(node *pipeline.Node) bool {
	for _, route := range node.Handoff {
		if route.Condition == "on_reject" || route.Condition == "always" {
			return true
		}
	}
	return false
}

func (eng *Engine) routeRunLocked(le *liveExec, run *NodeRun, node *pipeline.Node, routes []pipeline.HandoffRule, output string, octx *stageState, work *reworkState) {
	doc := structuredDoc(run, output)
	for _, route := range routes {
		input := output
		if route.Input != "" {
			input = renderHandoffTemplate(route.Input, run.Input, output, run.Comment, doc)
		}
		eng.deliverLocked(le, route.To, input, node.ID, octx, work)
	}
}

func renderHandoffTemplate(template, input, output, comment string, doc any) string {
	var rendered strings.Builder
	for i := 0; i < len(template); {
		start := strings.Index(template[i:], "{{")
		if start < 0 {
			rendered.WriteString(template[i:])
			break
		}
		start += i
		rendered.WriteString(template[i:start])
		end := strings.Index(template[start+2:], "}}") + start + 2
		name := strings.TrimSpace(template[start+2 : end])
		switch name {
		case "input":
			rendered.WriteString(input)
		case "output":
			rendered.WriteString(output)
		case "comment":
			rendered.WriteString(comment)
		default:
			if path, ok, err := schema.ParseFieldRef(name); ok && err == nil {
				rendered.WriteString(schema.Render(schema.Lookup(doc, path)))
			}
		}
		i = end + 2
	}
	return rendered.String()
}

func (eng *Engine) deactivateHandoffsLocked(le *liveExec, node *pipeline.Node, selected []pipeline.HandoffRule, octx *stageState, work *reworkState) {
	selectedCount := make(map[string]int, len(selected))
	for _, route := range selected {
		selectedCount[route.Condition+"\x00"+route.To]++
	}
	for _, route := range node.Handoff {
		key := route.Condition + "\x00" + route.To
		if selectedCount[key] > 0 {
			selectedCount[key]--
			continue
		}
		eng.deactivateTargetLocked(le, route.To, octx, work)
	}
}

func (eng *Engine) deactivateTargetLocked(le *liveExec, nodeID string, octx *stageState, work *reworkState) {
	expected, joins := &le.joinExpected, &le.joins
	if octx != nil {
		expected, joins = &octx.expected, &octx.joins
	}
	if work != nil {
		expected, joins = &work.counts, &work.joins
	}
	if (*expected)[nodeID] <= 0 {
		return
	}
	(*expected)[nodeID]--
	inputs := (*joins)[nodeID]
	node := le.p.Node(nodeID)
	if node.Type == pipeline.TypeOrchestrator && octx == nil && work == nil {
		if oi := le.orchs[nodeID]; oi != nil && !oi.readyClosed {
			oi.expected = (*expected)[nodeID]
			if len(oi.inputs) >= oi.expected {
				oi.readyClosed = true
				close(oi.inputsReady)
			}
		}
		if (*expected)[nodeID] == 0 && le.orchs[nodeID] == nil {
			for _, route := range node.Handoff {
				eng.deactivateTargetLocked(le, route.To, octx, work)
			}
		}
		return
	}
	if (*expected)[nodeID] == len(inputs) && len(inputs) > 0 {
		delete(*joins, nodeID)
		eng.dispatchNodeLocked(le, le.p.Node(nodeID), joinInputs(inputs), joinInputSources(inputs), octx, work)
		return
	}
	if (*expected)[nodeID] != 0 || len(inputs) != 0 {
		return
	}
	for _, route := range node.Handoff {
		eng.deactivateTargetLocked(le, route.To, octx, work)
	}
}

func joinInputSources(inputs []deliveredInput) string {
	froms := make([]string, len(inputs))
	for i, in := range inputs {
		froms[i] = in.from
	}
	return strings.Join(froms, ", ")
}

func (eng *Engine) finishRunErr(le *liveExec, run *NodeRun, node *pipeline.Node, octx *stageState, err error) {
	eng.mu.Lock()
	defer eng.mu.Unlock()
	delete(le.reworks, run.ID)
	now := time.Now().UTC()
	run.FinishedAt = &now
	if le.ctx != nil && le.ctx.Err() != nil && le.exec.Status.Terminal() {
		// The execution was already failed/canceled elsewhere; this run was
		// collateral damage of the context cancellation.
		run.Status = StatusCanceled
		run.Error = err.Error()
	} else {
		run.Status = StatusFailed
		run.Error = err.Error()
		log.Printf("execution %s: node %s run %s failed: %v", le.exec.ID, node.ID, run.ID, err)
		if !le.exec.Status.Terminal() {
			le.exec.Status = StatusFailed
			le.exec.Error = fmt.Sprintf("agent %q failed: %v", node.ID, err)
			if le.cancel != nil {
				le.cancel()
			}
		}
	}
	eng.releaseStageLocked(octx)
	eng.endRunLocked(le)
	eng.persistLocked(le)
}

// releaseStageLocked decrements the owning stage's outstanding counter and
// wakes the orchestrator when every chain has settled.
func (eng *Engine) releaseStageLocked(octx *stageState) {
	if octx == nil {
		return
	}
	octx.outstanding--
	if octx.outstanding <= 0 && !octx.closed {
		octx.closed = true
		close(octx.done)
	}
}

// endRunLocked settles the live-run counter and finishes the execution when
// the last run ends.
func (eng *Engine) endRunLocked(le *liveExec) {
	le.liveRuns--
	if le.liveRuns > 0 {
		return
	}
	if !le.exec.Status.Terminal() {
		le.exec.Status = StatusCompleted
	}
	now := time.Now().UTC()
	le.exec.FinishedAt = &now
	select {
	case <-le.done:
	default:
		close(le.done)
	}
}

// --- orchestrator execution ---

func (eng *Engine) runOrchestrator(le *liveExec, oi *orchInstance) {
	select {
	case <-oi.inputsReady:
	case <-le.ctx.Done():
		eng.mu.Lock()
		if !oi.run.Status.Terminal() {
			oi.run.Status = StatusCanceled
			now := time.Now().UTC()
			oi.run.FinishedAt = &now
		}
		eng.endRunLocked(le)
		eng.persistLocked(le)
		eng.mu.Unlock()
		return
	}

	eng.mu.Lock()
	now := time.Now().UTC()
	oi.run.Status = StatusRunning
	oi.run.StartedAt = &now
	input := joinInputs(oi.inputs)
	eng.persistLocked(le)
	eng.mu.Unlock()

	node := oi.node
	stageContext := input
	if !oi.run.AdditionalInputResolved {
		var err error
		stageContext, err = eng.awaitAdditionalInputs(le, oi.run, node, input)
		if err != nil {
			eng.finishRunErr(le, oi.run, node, nil, err)
			return
		}
	}
	if oi.startStage > 0 {
		stageContext = oi.resumeContext
	}
	for index, stage := range node.Stages {
		if index < oi.startStage {
			continue
		}
		eng.mu.Lock()
		if le.exec.Status.Terminal() {
			eng.mu.Unlock()
			eng.cancelOrchRun(le, oi)
			return
		}
		started := time.Now().UTC()
		sr := &StageRun{
			StageID:    stage.ID,
			Status:     StatusRunning,
			StartedAt:  &started,
			Snapshot:   snapshotLocked(le.exec),
			ownerRunID: oi.run.ID,
		}
		oi.run.Stages = append(oi.run.Stages, sr)
		scope := pipeline.StageScope(node.ID, stage.ID)
		oi.cur = &stageState{sr: sr, scope: scope, done: make(chan struct{}), expected: le.p.JoinCounts(scope)}
		for _, agentID := range stage.Agents {
			eng.deliverLocked(le, agentID, stageContext, node.ID, oi.cur)
		}
		eng.persistLocked(le)
		cur := oi.cur
		eng.mu.Unlock()

		select {
		case <-cur.done:
		case <-le.ctx.Done():
			eng.cancelOrchRun(le, oi)
			return
		}

		eng.mu.Lock()
		if le.exec.Status.Terminal() {
			sr.Status = StatusCanceled
			eng.mu.Unlock()
			eng.cancelOrchRun(le, oi)
			return
		}
		collected := joinStageOutputs(sr.Outputs)
		eng.mu.Unlock()

		if stage.Prompt != "" {
			out, err := eng.invokeOrchPrompt(le, oi.run, node, stage.Prompt, collected)
			if err != nil {
				eng.failStage(le, sr)
				eng.finishRunErr(le, oi.run, node, nil, fmt.Errorf("stage %q synthesis: %w", stage.ID, err))
				return
			}
			stageContext = out
		} else {
			stageContext = collected
		}

		eng.mu.Lock()
		finished := time.Now().UTC()
		sr.Context = stageContext
		sr.Status = StatusCompleted
		sr.FinishedAt = &finished
		eng.persistLocked(le)
		eng.mu.Unlock()
	}

	final := stageContext
	if node.Prompt != "" {
		out, err := eng.invokeOrchPrompt(le, oi.run, node, node.Prompt, final)
		if err != nil {
			eng.finishRunErr(le, oi.run, node, nil, fmt.Errorf("final synthesis: %w", err))
			return
		}
		final = out
	}
	eng.finishRunOK(le, oi.run, node, final, nil, "")
}

func (eng *Engine) invokeOrchPrompt(le *liveExec, run *NodeRun, node *pipeline.Node, prompt, input string) (string, error) {
	rt, err := eng.registry.Get(node.Runtime)
	if err != nil {
		return "", err
	}
	return eng.invokeRetrying(le, run, node.Retry, func() (string, error) {
		knowledgeInput, err := eng.withKnowledge(input, node.KnowledgeBases)
		if err != nil {
			return "", err
		}
		return rt.Invoke(le.ctx, runtime.Request{SystemPrompt: prompt, Input: knowledgeInput, Model: node.Model})
	})
}

func (eng *Engine) awaitAdditionalInputs(le *liveExec, run *NodeRun, node *pipeline.Node, input string) (string, error) {
	for {
		eng.mu.Lock()
		combined, ready := collectAdditionalInputs(le, node, input)
		changed := le.outputsChanged
		if ready {
			run.Input = combined
			run.AdditionalInputResolved = true
			eng.persistLocked(le)
			eng.mu.Unlock()
			return combined, nil
		}
		eng.mu.Unlock()

		select {
		case <-changed:
		case <-le.ctx.Done():
			return "", le.ctx.Err()
		}
	}
}

func collectAdditionalInputs(le *liveExec, node *pipeline.Node, input string) (string, bool) {
	if len(node.AdditionalInput) == 0 {
		return input, true
	}
	outputs := make([]string, len(node.AdditionalInput))
	included := make([]bool, len(node.AdditionalInput))
	for i, sourceID := range node.AdditionalInput {
		marker := fmt.Sprintf("--- additional input from %s ---", sourceID)
		if strings.Contains(input, marker) {
			included[i] = true
			continue
		}
		found := false
		for runIndex := len(le.exec.Runs) - 1; runIndex >= 0; runIndex-- {
			source := le.exec.Runs[runIndex]
			if source.NodeID != sourceID {
				continue
			}
			if source.Status != StatusCompleted {
				return "", false
			}
			outputs[i] = source.Output
			found = true
			break
		}
		if !found {
			return "", false
		}
	}

	var combined strings.Builder
	combined.WriteString(input)
	for i, sourceID := range node.AdditionalInput {
		if included[i] {
			continue
		}
		if combined.Len() > 0 {
			combined.WriteString("\n\n")
		}
		fmt.Fprintf(&combined, "--- additional input from %s ---\n%s", sourceID, outputs[i])
	}
	return combined.String(), true
}

func joinCountsFrom(p *pipeline.Pipeline, root string) map[string]int {
	counts := map[string]int{root: 1}
	seen := map[string]bool{}
	queue := []string{root}
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		if seen[id] {
			continue
		}
		seen[id] = true
		for _, target := range p.Node(id).HandoffTargets() {
			counts[target]++
			queue = append(queue, target)
		}
	}
	return counts
}

// invokeRetrying runs an agent call. On failure the run is parked in
// waiting_retry until ResolveFailure is called or the execution ends; the
// returned error means the failure was abandoned or the execution canceled.
// The caller's stage and live-run counters stay held while parked, so
// parallel branches continue but nothing downstream proceeds without it.
func (eng *Engine) invokeRetrying(le *liveExec, run *NodeRun, policy *pipeline.RetryPolicy, invoke func() (string, error)) (string, error) {
	return eng.invokeWithRetries(le, run, policy, true, invoke)
}

// invokeWithRetries is invokeRetrying with control over the call slot.
// Pipeline nodes do not take one: their child execution's own agent calls
// need the slots, and holding one while waiting could deadlock.
func (eng *Engine) invokeWithRetries(le *liveExec, run *NodeRun, policy *pipeline.RetryPolicy, useSlot bool, invoke func() (string, error)) (string, error) {
	var maxRetries int
	var backoff time.Duration
	if policy != nil {
		maxRetries = policy.Count
		var err error
		backoff, err = policy.BackoffDuration()
		if err != nil {
			return "", fmt.Errorf("invalid retry backoff: %w", err)
		}
	}
	automaticRetries := 0
	for {
		if err := le.ctx.Err(); err != nil {
			return "", err
		}
		if useSlot {
			select {
			case eng.callSlots <- struct{}{}:
			case <-le.ctx.Done():
				return "", le.ctx.Err()
			}
		}
		var release func() error
		if useSlot && eng.store != nil && eng.store.owner != "" {
			var err error
			release, err = eng.store.acquireSlot(le.ctx, cap(eng.callSlots))
			if err != nil {
				<-eng.callSlots
				return "", err
			}
		}
		out, err := invoke()
		if release != nil {
			if releaseErr := release(); releaseErr != nil {
				eng.mu.Lock()
				eng.failPersistenceLocked(fmt.Errorf("releasing runtime slot: %w", releaseErr))
				eng.mu.Unlock()
				<-eng.callSlots
				return "", releaseErr
			}
		}
		if useSlot {
			<-eng.callSlots
		}
		if err == nil {
			return out, nil
		}
		var outcome *runtime.CodeOutcomeError
		if errors.As(err, &outcome) {
			return out, err
		}
		var rejected *childRejectedError
		if errors.As(err, &rejected) {
			return out, err
		}

		if automaticRetries < maxRetries {
			eng.mu.Lock()
			if le.ctx.Err() != nil || le.exec.Status.Terminal() {
				eng.mu.Unlock()
				return "", err
			}
			automaticRetries++
			run.Retries++
			run.Error = err.Error()
			log.Printf("execution %s: node %s run %s call failed; automatic retry %d/%d: %v",
				le.exec.ID, run.NodeID, run.ID, automaticRetries, maxRetries, err)
			eng.persistLocked(le)
			eng.mu.Unlock()
			if backoff > 0 {
				timer := time.NewTimer(backoff)
				select {
				case <-timer.C:
				case <-le.ctx.Done():
					if !timer.Stop() {
						select {
						case <-timer.C:
						default:
						}
					}
					return "", le.ctx.Err()
				}
			}
			continue
		}

		eng.mu.Lock()
		if le.ctx.Err() != nil || le.exec.Status.Terminal() {
			eng.mu.Unlock()
			return "", err
		}
		pending := &pendingRetry{run: run, resume: run.Status, ch: make(chan bool, 1)}
		run.Status = StatusWaitingRetry
		run.Error = err.Error()
		log.Printf("execution %s: node %s run %s call failed; awaiting manual retry: %v",
			le.exec.ID, run.NodeID, run.ID, err)
		le.retries[run.ID] = pending
		eng.persistLocked(le)
		eng.mu.Unlock()

		var retry bool
		select {
		case retry = <-pending.ch:
		case <-le.ctx.Done():
			eng.mu.Lock()
			delete(le.retries, run.ID)
			eng.mu.Unlock()
			return "", err
		}
		if !retry {
			return "", err
		}
	}
}

func (eng *Engine) failStage(le *liveExec, sr *StageRun) {
	eng.mu.Lock()
	now := time.Now().UTC()
	sr.Status = StatusFailed
	sr.FinishedAt = &now
	eng.persistLocked(le)
	eng.mu.Unlock()
}

func (eng *Engine) cancelOrchRun(le *liveExec, oi *orchInstance) {
	eng.mu.Lock()
	if !oi.run.Status.Terminal() {
		oi.run.Status = StatusCanceled
		now := time.Now().UTC()
		oi.run.FinishedAt = &now
	}
	eng.endRunLocked(le)
	eng.persistLocked(le)
	eng.mu.Unlock()
}

// --- helpers ---

func (eng *Engine) persistLocked(le *liveExec) {
	if eng.persistError != nil {
		return
	}
	if eng.store != nil {
		if err := eng.store.Save(le.exec); err != nil {
			log.Printf("persisting execution %s: %v", le.exec.ID, err)
			if eng.store.owner != "" {
				eng.failPersistenceLocked(err)
			}
			return
		}
		select {
		case <-le.done:
			delete(eng.execs, le.exec.ID)
		default:
		}
	}
}

func (eng *Engine) failPersistenceLocked(err error) {
	if eng.persistError == nil {
		eng.persistError = err
	}
	for _, active := range eng.execs {
		active.cancel()
	}
}

func joinInputs(inputs []deliveredInput) string {
	if len(inputs) == 1 {
		return inputs[0].input
	}
	var b strings.Builder
	for i, in := range inputs {
		if i > 0 {
			b.WriteString("\n\n")
		}
		from := in.from
		if from == "" {
			from = "initial input"
		}
		fmt.Fprintf(&b, "--- handoff from %s ---\n%s", from, in.input)
	}
	return b.String()
}

func joinStageOutputs(outputs []StageOutput) string {
	if len(outputs) == 1 {
		return outputs[0].Output
	}
	var b strings.Builder
	for i, o := range outputs {
		if i > 0 {
			b.WriteString("\n\n")
		}
		fmt.Fprintf(&b, "--- result from %s ---\n%s", o.NodeID, o.Output)
	}
	return b.String()
}

func firstChars(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func newExecID() string {
	return time.Now().UTC().Format("20060102-150405") + "-" + randHex(4)
}

func newRunID() string { return randHex(8) }

func randHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}
