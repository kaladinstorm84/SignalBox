// Package engine executes pipelines: it dispatches handoffs between standard
// agents, coordinates orchestrator stages with fan-in joins, pauses at human
// approval gates, and persists every state change.
package engine

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gitea/signalbox/internal/pipeline"
	"github.com/gitea/signalbox/internal/runtime"
)

// Engine runs executions. One mutex guards all execution state; runtime
// invocations (the slow part) happen outside the lock.
type Engine struct {
	mu        sync.Mutex
	pipelines map[string]*pipeline.Pipeline
	registry  *runtime.Registry
	store     *Store
	execs     map[string]*liveExec
}

// liveExec is the in-memory state wrapped around a persisted Execution.
type liveExec struct {
	exec      *Execution
	p         *pipeline.Pipeline
	ctx       context.Context
	cancel    context.CancelFunc
	liveRuns  int
	done      chan struct{}
	approvals map[string]chan approvalDecision
	orchs     map[string]*orchInstance
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
	outstanding int
	done        chan struct{}
	closed      bool
}

// New builds an engine and loads persisted execution history.
func New(pipelines map[string]*pipeline.Pipeline, registry *runtime.Registry, store *Store) (*Engine, error) {
	eng := &Engine{
		pipelines: pipelines,
		registry:  registry,
		store:     store,
		execs:     map[string]*liveExec{},
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
func (eng *Engine) Pipelines() map[string]*pipeline.Pipeline { return eng.pipelines }

// Start begins a new execution of the named pipeline and returns its ID.
func (eng *Engine) Start(pipelineName, input string) (string, error) {
	p, ok := eng.pipelines[pipelineName]
	if !ok {
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
		p:         p,
		ctx:       ctx,
		cancel:    cancel,
		done:      make(chan struct{}),
		approvals: map[string]chan approvalDecision{},
		orchs:     map[string]*orchInstance{},
	}

	eng.mu.Lock()
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
		waiting := 0
		for _, r := range le.exec.Runs {
			if r.Status == StatusWaitingApproval {
				waiting++
			}
		}
		out = append(out, Summary{
			ID:         le.exec.ID,
			Pipeline:   le.exec.Pipeline,
			Input:      firstChars(le.exec.Input, 160),
			Status:     le.exec.Status,
			Waiting:    waiting,
			CreatedAt:  le.exec.CreatedAt,
			FinishedAt: le.exec.FinishedAt,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out
}

// WaitingApprovals returns the runs currently blocked on human approval.
func (eng *Engine) WaitingApprovals(execID string) []*NodeRun {
	eng.mu.Lock()
	defer eng.mu.Unlock()
	le, ok := eng.execs[execID]
	if !ok {
		return nil
	}
	var out []*NodeRun
	for _, r := range le.exec.Runs {
		if r.Status == StatusWaitingApproval {
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

	run := &NodeRun{
		ID:         newRunID(),
		NodeID:     nodeID,
		Type:       node.Type,
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

	rt, err := eng.registry.Get(node.Runtime)
	if err != nil {
		eng.finishRunErr(le, run, node, octx, err)
		return
	}
	out, err := rt.Invoke(le.ctx, runtime.Request{SystemPrompt: node.Prompt, Input: run.Input})
	if err != nil {
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

	eng.mu.Lock()
	finished := time.Now().UTC()
	run.Status = StatusRejected
	run.FinishedAt = &finished
	if !le.exec.Status.Terminal() {
		le.exec.Status = StatusRejected
		le.exec.Error = fmt.Sprintf("approval %q was rejected", node.ID)
		if le.cancel != nil {
			le.cancel()
		}
	}
	eng.releaseStageLocked(octx)
	eng.endRunLocked(le)
	eng.persistLocked(le)
	eng.mu.Unlock()
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
	stageContext := joinInputs(oi.inputs)
	oi.run.Input = stageContext
	eng.persistLocked(le)
	eng.mu.Unlock()

	node := oi.node
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
		oi.cur = &stageState{sr: sr, done: make(chan struct{})}
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
			out, err := eng.invokeOrchPrompt(le, node, stage.Prompt, collected)
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
		out, err := eng.invokeOrchPrompt(le, node, node.Prompt, final)
		if err != nil {
			eng.finishRunErr(le, oi.run, node, nil, fmt.Errorf("final synthesis: %w", err))
			return
		}
		final = out
	}
	eng.finishRunOK(le, oi.run, node.Handoff, final, nil)
}

func (eng *Engine) invokeOrchPrompt(le *liveExec, node *pipeline.Node, prompt, input string) (string, error) {
	rt, err := eng.registry.Get(node.Runtime)
	if err != nil {
		return "", err
	}
	return rt.Invoke(le.ctx, runtime.Request{SystemPrompt: prompt, Input: input})
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
