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
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/kaladinstorm84/SignalBox/internal/pipeline"
	"github.com/kaladinstorm84/SignalBox/internal/runtime"
)

// Engine runs executions. One mutex guards all execution state; runtime
// invocations (the slow part) happen outside the lock.
type Engine struct {
	mu          sync.Mutex
	pipelines   map[string]*pipeline.Pipeline
	registry    *runtime.Registry
	store       *Store
	execs       map[string]*liveExec
	callSlots   chan struct{}
	projectRoot string
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
	retries        map[string]*pendingRetry
	orchs          map[string]*orchInstance
	outputsChanged chan struct{}
	recoveryInputs map[string]string
	recoveryJoins  map[string]int
	// joins holds fan-in nodes outside any stage still waiting for inputs.
	joins map[string][]deliveredInput
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

// orchInstance is the singleton per-execution state of one orchestrator node.
type orchInstance struct {
	node        *pipeline.Node
	run         *NodeRun
	expected    int
	inputs      []deliveredInput
	inputsReady chan struct{}
	readyClosed bool
	cur         *stageState
}

type deliveredInput struct {
	from  string
	input string
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
}

// New builds an engine and loads persisted execution history.
func New(pipelines map[string]*pipeline.Pipeline, registry *runtime.Registry, store *Store) (*Engine, error) {
	return NewWithLimit(pipelines, registry, store, 4)
}

// NewWithLimit builds an engine with a maximum number of concurrent runtime calls.
func NewWithLimit(pipelines map[string]*pipeline.Pipeline, registry *runtime.Registry, store *Store, maxConcurrentCalls int, projectRoot ...string) (*Engine, error) {
	if maxConcurrentCalls < 1 {
		return nil, fmt.Errorf("maximum concurrent calls must be at least 1")
	}
	eng := &Engine{
		pipelines: pipelines,
		registry:  registry,
		store:     store,
		execs:     map[string]*liveExec{},
		callSlots: make(chan struct{}, maxConcurrentCalls),
	}
	if len(projectRoot) > 0 {
		eng.projectRoot = projectRoot[0]
	}
	if store != nil {
		history, err := store.LoadAll()
		if err != nil {
			return nil, fmt.Errorf("loading execution history: %w", err)
		}
		for _, e := range history {
			done := make(chan struct{})
			close(done)
			eng.execs[e.ID] = &liveExec{exec: e, done: done}
		}
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
func (eng *Engine) Start(pipelineName, input string) (string, error) {
	eng.mu.Lock()
	p, ok := eng.pipelines[pipelineName]
	if !ok {
		eng.mu.Unlock()
		return "", fmt.Errorf("unknown pipeline %q", pipelineName)
	}
	ctx, cancel := context.WithCancel(context.Background())
	le := &liveExec{
		exec: &Execution{
			ID:        newExecID(),
			Pipeline:  pipelineName,
			Input:     input,
			Status:    StatusRunning,
			CreatedAt: time.Now().UTC(),
		},
		p:              p,
		ctx:            ctx,
		cancel:         cancel,
		done:           make(chan struct{}),
		approvals:      map[string]chan approvalDecision{},
		retries:        map[string]*pendingRetry{},
		orchs:          map[string]*orchInstance{},
		outputsChanged: make(chan struct{}),
	}

	eng.execs[le.exec.ID] = le
	eng.deliverLocked(le, p.Entry, input, "", nil)
	eng.persistLocked(le)
	eng.mu.Unlock()
	return le.exec.ID, nil
}

// Approve resolves a pending approval gate.
func (eng *Engine) Approve(execID, runID string, approved bool, approvedBy, comment string) error {
	eng.mu.Lock()
	defer eng.mu.Unlock()
	le, ok := eng.execs[execID]
	if !ok {
		return fmt.Errorf("unknown execution %q", execID)
	}
	ch, ok := le.approvals[runID]
	if !ok {
		return fmt.Errorf("no pending approval %q in execution %q", runID, execID)
	}
	delete(le.approvals, runID)
	ch <- approvalDecision{approved: approved, approvedBy: approvedBy, comment: comment}
	return nil
}

// ResolveFailure resumes a run paused after a failed agent call: retry
// re-invokes the agent with the same input; otherwise the execution fails.
func (eng *Engine) ResolveFailure(execID, runID string, retry bool) error {
	eng.mu.Lock()
	defer eng.mu.Unlock()
	le, ok := eng.execs[execID]
	if !ok {
		return fmt.Errorf("unknown execution %q", execID)
	}
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

// RestartFromInterrupted starts a new execution at a persisted interrupted
// node, reusing its saved input and completed outputs for additional inputs.
func (eng *Engine) RestartFromInterrupted(execID, runID string) (string, error) {
	eng.mu.Lock()
	defer eng.mu.Unlock()

	source, ok := eng.execs[execID]
	if !ok {
		return "", fmt.Errorf("unknown execution %q", execID)
	}
	if source.exec.Status != StatusInterrupted {
		return "", fmt.Errorf("execution %q is not interrupted", execID)
	}
	var interrupted *NodeRun
	for _, run := range source.exec.Runs {
		if run.ID == runID {
			interrupted = run
			break
		}
	}
	if interrupted == nil || interrupted.Status != StatusInterrupted {
		return "", fmt.Errorf("run %q is not interrupted in execution %q", runID, execID)
	}
	if interrupted.Type != pipeline.TypeStandard && interrupted.Type != pipeline.TypeCode {
		return "", fmt.Errorf("run %q is not a retryable agent call", runID)
	}
	p, ok := eng.pipelines[source.exec.Pipeline]
	if !ok {
		return "", fmt.Errorf("pipeline %q is no longer available", source.exec.Pipeline)
	}
	node := p.Node(interrupted.NodeID)
	if node == nil {
		return "", fmt.Errorf("agent %q is no longer in pipeline %q", interrupted.NodeID, source.exec.Pipeline)
	}
	if node.Type != pipeline.TypeStandard && node.Type != pipeline.TypeCode {
		return "", fmt.Errorf("agent %q is no longer a callable node", node.ID)
	}

	ctx, cancel := context.WithCancel(context.Background())
	le := &liveExec{
		exec: &Execution{
			ID:        newExecID(),
			Pipeline:  source.exec.Pipeline,
			Input:     source.exec.Input,
			Status:    StatusRunning,
			CreatedAt: time.Now().UTC(),
			Runs:      []*NodeRun{},
		},
		p:              p,
		ctx:            ctx,
		cancel:         cancel,
		done:           make(chan struct{}),
		approvals:      map[string]chan approvalDecision{},
		retries:        map[string]*pendingRetry{},
		orchs:          map[string]*orchInstance{},
		outputsChanged: make(chan struct{}),
		recoveryInputs: map[string]string{},
		recoveryJoins:  joinCountsFrom(p, node.ID),
	}
	for _, previous := range source.exec.Runs {
		if previous.Status == StatusCompleted {
			le.recoveryInputs[previous.NodeID] = previous.Output
		}
	}
	rootRun := &NodeRun{
		ID:                      newRunID(),
		NodeID:                  node.ID,
		Type:                    node.Type,
		Status:                  StatusPending,
		Input:                   interrupted.Input,
		Format:                  node.Format,
		AdditionalInputResolved: interrupted.AdditionalInputResolved,
		FromNodeID:              interrupted.FromNodeID,
		StageID:                 interrupted.StageID,
	}
	le.exec.Runs = append(le.exec.Runs, rootRun)
	le.liveRuns = 1
	eng.execs[le.exec.ID] = le
	if node.Type == pipeline.TypeOrchestrator {
		ready := make(chan struct{})
		close(ready)
		orchestrator := &orchInstance{
			node:        node,
			run:         rootRun,
			inputs:      []deliveredInput{{from: interrupted.FromNodeID, input: interrupted.Input}},
			inputsReady: ready,
			readyClosed: true,
		}
		le.orchs[node.ID] = orchestrator
		go eng.runOrchestrator(le, orchestrator)
	} else {
		go eng.runNode(le, rootRun, node, nil)
	}
	eng.persistLocked(le)
	return le.exec.ID, nil
}

// Cancel aborts a running execution.
func (eng *Engine) Cancel(execID string) error {
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
	eng.persistLocked(le)
	return nil
}

// Delete removes a finished execution from memory and disk. Executions that
// are still running (including canceled ones whose agent calls have not yet
// wound down) cannot be deleted.
func (eng *Engine) Delete(execID string) error {
	eng.mu.Lock()
	defer eng.mu.Unlock()
	le, ok := eng.execs[execID]
	if !ok {
		return fmt.Errorf("unknown execution %q", execID)
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
	delete(eng.execs, execID)
	return nil
}

// Get returns a deep copy of an execution, safe to serialise concurrently.
func (eng *Engine) Get(execID string) (*Execution, error) {
	eng.mu.Lock()
	defer eng.mu.Unlock()
	le, ok := eng.execs[execID]
	if !ok {
		return nil, fmt.Errorf("unknown execution %q", execID)
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
	out := make([]Summary, 0, len(eng.execs))
	for _, le := range eng.execs {
		waiting, retrying := 0, 0
		for _, r := range le.exec.Runs {
			switch r.Status {
			case StatusWaitingApproval:
				waiting++
			case StatusWaitingRetry:
				retrying++
			}
		}
		out = append(out, Summary{
			ID:         le.exec.ID,
			Pipeline:   le.exec.Pipeline,
			Input:      firstChars(le.exec.Input, 160),
			Status:     le.exec.Status,
			Waiting:    waiting,
			Retrying:   retrying,
			CreatedAt:  le.exec.CreatedAt,
			FinishedAt: le.exec.FinishedAt,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out
}

// WaitingApprovals returns the runs currently blocked on human approval.
func (eng *Engine) WaitingApprovals(execID string) []*NodeRun {
	return eng.runsWithStatus(execID, StatusWaitingApproval)
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
		return nil
	}
	var out []*NodeRun
	for _, r := range le.exec.Runs {
		if r.Status == status {
			cp := *r
			out = append(out, &cp)
		}
	}
	return out
}

// Wait returns a channel closed when the execution finishes.
func (eng *Engine) Wait(execID string) (<-chan struct{}, error) {
	eng.mu.Lock()
	defer eng.mu.Unlock()
	le, ok := eng.execs[execID]
	if !ok {
		return nil, fmt.Errorf("unknown execution %q", execID)
	}
	return le.done, nil
}

// --- dispatch ---

// deliverLocked routes a handoff payload to a node. Caller holds eng.mu.
// octx is non-nil when the delivery happens inside an orchestrator stage.
func (eng *Engine) deliverLocked(le *liveExec, nodeID, input, from string, octx *stageState) {
	if le.exec.Status.Terminal() {
		return
	}
	node := le.p.Node(nodeID)

	if node.Type == pipeline.TypeOrchestrator {
		oi := le.orchs[nodeID]
		if oi == nil {
			expected := le.p.InboundCount(nodeID)
			if nodeID == le.p.Entry {
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
	scope, joins := pipeline.TopScope, &le.joins
	if octx != nil {
		scope, joins = octx.scope, &octx.joins
	}
	expected := le.p.JoinCount(scope, nodeID)
	if octx == nil && le.recoveryJoins != nil {
		expected = le.recoveryJoins[nodeID]
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

	run := &NodeRun{
		ID:         newRunID(),
		NodeID:     nodeID,
		Type:       node.Type,
		Format:     node.Format,
		Status:     StatusPending,
		Input:      input,
		FromNodeID: from,
	}
	if octx != nil {
		run.StageID = octx.sr.StageID
		run.OrchRunID = octx.sr.ownerRunID
		octx.outstanding++
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

	eng.mu.Lock()
	now := time.Now().UTC()
	run.Status = StatusRunning
	run.StartedAt = &now
	eng.persistLocked(le)
	eng.mu.Unlock()

	out, err := eng.invokeRetrying(le, run, func() (string, error) {
		if node.Type == pipeline.TypeCode {
			rt, err := runtime.NewCode(node, eng.projectRoot)
			if err != nil {
				return "", err
			}
			return rt.Invoke(le.ctx, runtime.Request{Input: run.Input})
		}
		rt, err := eng.registry.Get(node.Runtime)
		if err != nil {
			return "", err
		}
		return rt.Invoke(le.ctx, runtime.Request{SystemPrompt: node.Prompt, Input: run.Input})
	})
	if err != nil {
		var outcome *runtime.CodeOutcomeError
		if errors.As(err, &outcome) && outcome.Outcome == "retry" {
			eng.finishRunRejected(le, run, node, octx, outcome.Feedback, out)
			return
		}
		eng.finishRunErr(le, run, node, octx, err)
		return
	}
	eng.finishRunOK(le, run, node.Handoff, out, octx)
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
	eng.mu.Unlock()

	if decision.approved {
		// An approval gate passes its payload through unchanged.
		eng.finishRunOK(le, run, node.Handoff, run.Input, octx)
		return
	}
	eng.finishRunRejected(le, run, node, octx, decision.comment, "")
}

func (eng *Engine) finishRunRejected(le *liveExec, run *NodeRun, node *pipeline.Node, octx *stageState, note, output string) {
	eng.mu.Lock()
	finished := time.Now().UTC()
	run.Status = StatusRejected
	run.FinishedAt = &finished
	run.Comment = note
	run.Output = output
	if node.Type == pipeline.TypeCode {
		run.Error = note
	}
	if !le.exec.Status.Terminal() && eng.reworkAfterRejectionLocked(le, run, node, octx, note) {
		eng.releaseStageLocked(octx)
		eng.endRunLocked(le)
		eng.persistLocked(le)
		eng.mu.Unlock()
		return
	}
	if !le.exec.Status.Terminal() {
		le.exec.Status = StatusRejected
		le.exec.Error = fmt.Sprintf("node %q was rejected", node.ID)
		if le.cancel != nil {
			le.cancel()
		}
	}
	eng.releaseStageLocked(octx)
	eng.endRunLocked(le)
	eng.persistLocked(le)
	eng.mu.Unlock()
}

func (eng *Engine) reworkAfterRejectionLocked(le *liveExec, rejected *NodeRun, node *pipeline.Node, octx *stageState, note string) bool {
	runs := le.exec.Runs
	rejectedIndex := len(runs)
	for i, run := range runs {
		if run.ID == rejected.ID {
			rejectedIndex = i
			break
		}
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
		for _, target := range checkpoint.Handoff {
			eng.deliverLocked(le, target, input, checkpoint.ID, octx)
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
			eng.deliverLocked(le, previous.NodeID, appendRejectionNote(previous.Input, node.ID, note), previous.FromNodeID, octx)
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
		queue = append(queue, p.Node(id).Handoff...)
	}
	return false
}

// finishRunOK records success, dispatches handoffs (or hands back to the
// owning orchestrator stage / records a final output), and settles counters.
func (eng *Engine) finishRunOK(le *liveExec, run *NodeRun, handoffs []string, output string, octx *stageState) {
	eng.mu.Lock()
	defer eng.mu.Unlock()
	now := time.Now().UTC()
	run.Output = output
	run.Status = StatusCompleted
	run.FinishedAt = &now
	close(le.outputsChanged)
	le.outputsChanged = make(chan struct{})

	if !le.exec.Status.Terminal() {
		if len(handoffs) > 0 {
			for _, target := range handoffs {
				eng.deliverLocked(le, target, output, run.NodeID, octx)
			}
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

func (eng *Engine) finishRunErr(le *liveExec, run *NodeRun, node *pipeline.Node, octx *stageState, err error) {
	eng.mu.Lock()
	defer eng.mu.Unlock()
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
	for _, stage := range node.Stages {
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
			ownerRunID: oi.run.ID,
		}
		oi.run.Stages = append(oi.run.Stages, sr)
		oi.cur = &stageState{sr: sr, scope: pipeline.StageScope(node.ID, stage.ID), done: make(chan struct{})}
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
	eng.finishRunOK(le, oi.run, node.Handoff, final, nil)
}

func (eng *Engine) invokeOrchPrompt(le *liveExec, run *NodeRun, node *pipeline.Node, prompt, input string) (string, error) {
	return eng.invokeRetrying(le, run, func() (string, error) {
		rt, err := eng.registry.Get(node.Runtime)
		if err != nil {
			return "", err
		}
		return rt.Invoke(le.ctx, runtime.Request{SystemPrompt: prompt, Input: input})
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
			output, ok := le.recoveryInputs[sourceID]
			if !ok {
				return "", false
			}
			outputs[i] = output
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
		for _, target := range p.Node(id).Handoff {
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
func (eng *Engine) invokeRetrying(le *liveExec, run *NodeRun, invoke func() (string, error)) (string, error) {
	for {
		select {
		case eng.callSlots <- struct{}{}:
		case <-le.ctx.Done():
			return "", le.ctx.Err()
		}
		out, err := invoke()
		<-eng.callSlots
		if err == nil {
			return out, nil
		}
		var outcome *runtime.CodeOutcomeError
		if errors.As(err, &outcome) {
			return out, err
		}

		eng.mu.Lock()
		if le.ctx.Err() != nil || le.exec.Status.Terminal() {
			eng.mu.Unlock()
			return "", err
		}
		pending := &pendingRetry{run: run, resume: run.Status, ch: make(chan bool, 1)}
		run.Status = StatusWaitingRetry
		run.Error = err.Error()
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
	if eng.store != nil {
		_ = eng.store.Save(le.exec)
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
