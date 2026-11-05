package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/kaladinstorm84/SignalBox/internal/pipeline"
)

// Definition fingerprints a resolved pipeline (after parameter substitution).
// Node hashes cover every field of the agent definition; files referenced by
// code steps are not hashed.
type Definition struct {
	Hash  string            `json:"hash"`
	Entry string            `json:"entry"`
	Nodes map[string]string `json:"nodes"`
}

// RunSnapshot is execution state that later runs may change and that a
// restart must roll back. Run inputs, context selections, and session
// transcripts are already persisted on the run itself.
type RunSnapshot struct {
	Chunks    []Chunk `json:"chunks,omitempty"`
	NextChunk int     `json:"next_chunk,omitempty"`
}

// Recovery describes where a restarted execution came from.
type Recovery struct {
	SourceExecution string `json:"source_execution"`
	SourceRun       string `json:"source_run"`
	NodeID          string `json:"node_id"`
	Stage           string `json:"stage,omitempty"`
	// Resumed is set when an interrupted session continues its transcript
	// instead of starting the discussion again.
	Resumed bool `json:"resumed,omitempty"`
	// ReusedRuns counts completed runs carried over instead of repeated.
	ReusedRuns int `json:"reused_runs"`
	// Forced is set when the restart was allowed despite definition changes.
	Forced   bool      `json:"forced,omitempty"`
	Warnings []string  `json:"warnings,omitempty"`
	At       time.Time `json:"at"`
}

// RestartOptions selects the restart point. With no RunID or NodeID the
// most recent unfinished run is chosen (see RestartPoint).
type RestartOptions struct {
	RunID string
	// NodeID selects the latest top-level run of that agent.
	NodeID string
	// Stage restarts an orchestrator run from the named stage, reusing the
	// context saved by the stage before it.
	Stage string
	// Force restarts even if the pipeline definition has changed.
	Force bool
}

// DefinitionChangedError reports pipeline edits made since the source
// execution ran. Restart again with Force to use the current definition.
type DefinitionChangedError struct {
	Pipeline  string
	Execution string
	Changes   []string
}

func (e *DefinitionChangedError) Error() string {
	return fmt.Sprintf("pipeline %q has changed since execution %s ran (%s); restart with force to use the current definition",
		e.Pipeline, e.Execution, strings.Join(e.Changes, "; "))
}

func fingerprint(p *pipeline.Pipeline) *Definition {
	def := &Definition{Entry: p.Entry, Nodes: make(map[string]string, len(p.Nodes))}
	ids := make([]string, 0, len(p.Nodes))
	for i := range p.Nodes {
		node := &p.Nodes[i]
		data, err := json.Marshal(node)
		if err != nil {
			data = []byte(fmt.Sprintf("%#v", *node))
		}
		sum := sha256.Sum256(data)
		def.Nodes[node.ID] = hex.EncodeToString(sum[:])
		ids = append(ids, node.ID)
	}
	sort.Strings(ids)
	h := sha256.New()
	fmt.Fprintf(h, "entry=%s\n", p.Entry)
	for _, id := range ids {
		fmt.Fprintf(h, "%s=%s\n", id, def.Nodes[id])
	}
	def.Hash = hex.EncodeToString(h.Sum(nil))
	return def
}

// definitionChanges lists differences between two fingerprints.
func definitionChanges(old, cur *Definition) []string {
	if old == nil || cur == nil || old.Hash == cur.Hash {
		return nil
	}
	var changes []string
	if old.Entry != cur.Entry {
		changes = append(changes, fmt.Sprintf("entry changed from %q to %q", old.Entry, cur.Entry))
	}
	var modified, removed, added []string
	for id, hash := range old.Nodes {
		current, ok := cur.Nodes[id]
		switch {
		case !ok:
			removed = append(removed, id)
		case current != hash:
			modified = append(modified, id)
		}
	}
	for id := range cur.Nodes {
		if _, ok := old.Nodes[id]; !ok {
			added = append(added, id)
		}
	}
	for _, group := range []struct {
		label string
		ids   []string
	}{{"modified", modified}, {"removed", removed}, {"added", added}} {
		if len(group.ids) > 0 {
			sort.Strings(group.ids)
			changes = append(changes, fmt.Sprintf("%s agents: %s", group.label, strings.Join(group.ids, ", ")))
		}
	}
	return changes
}

func snapshotLocked(e *Execution) *RunSnapshot {
	if len(e.Chunks) == 0 && e.NextChunk == 0 {
		return nil
	}
	return &RunSnapshot{Chunks: append([]Chunk(nil), e.Chunks...), NextChunk: e.NextChunk}
}

// RestartPoint picks the run a restart should begin from when none is
// given: the most recent failed, interrupted, or canceled run (or, for a
// rejected execution, the rejecting run). Runs inside an orchestrator stage
// map to their orchestrator run and stage.
func RestartPoint(e *Execution) (runID, stage string, err error) {
	pick := func(statuses ...Status) *NodeRun {
		for i := len(e.Runs) - 1; i >= 0; i-- {
			for _, status := range statuses {
				if e.Runs[i].Status == status {
					return e.Runs[i]
				}
			}
		}
		return nil
	}
	run := pick(StatusFailed, StatusInterrupted, StatusCanceled, StatusWaitingRetry)
	if run == nil && e.Status == StatusRejected {
		run = pick(StatusRejected)
	}
	if run == nil {
		return "", "", fmt.Errorf("execution %q has no failed, interrupted, or canceled run; choose a run to restart from", e.ID)
	}
	if run.StageID != "" && run.OrchRunID != "" {
		return run.OrchRunID, run.StageID, nil
	}
	return run.ID, "", nil
}

// RestartFromInterrupted starts a new execution at a persisted interrupted
// node. It is Restart limited to interrupted runs, kept for API stability.
func (eng *Engine) RestartFromInterrupted(execID, runID string) (string, error) {
	source, err := eng.Get(execID)
	if err != nil {
		return "", err
	}
	if source.Status != StatusInterrupted {
		return "", fmt.Errorf("execution %q is not interrupted", execID)
	}
	for _, run := range source.Runs {
		if run.ID == runID {
			if run.Status != StatusInterrupted {
				break
			}
			if !restartableType(run.Type) {
				return "", fmt.Errorf("run %q is not a retryable agent call", runID)
			}
			return eng.Restart(execID, RestartOptions{RunID: runID})
		}
	}
	return "", fmt.Errorf("run %q is not interrupted in execution %q", runID, execID)
}

// restartableType reports whether interrupted runs of a node type can be
// resumed in place by RestartFromInterrupted.
func restartableType(t pipeline.NodeType) bool {
	switch t {
	case pipeline.TypeStandard, pipeline.TypeCode, pipeline.TypeLLMGate, pipeline.TypeSession:
		return true
	}
	return false
}

// Restart starts a new execution from a selected run of a finished one.
//
// The selected node runs again with its saved input, context selection, and
// chunk snapshot, and everything downstream of it runs again as it is
// reached. Completed runs that are not downstream of the restart point are
// copied into the new execution (marked reused) rather than repeated; their
// handoffs into re-run joins are replayed so fan-in nodes still receive
// every input. An interrupted session resumes its saved transcript.
//
// If the pipeline definition changed since the source ran, Restart returns
// a *DefinitionChangedError unless opts.Force is set, in which case the
// changes are recorded as warnings on the new execution.
func (eng *Engine) Restart(execID string, opts RestartOptions) (string, error) {
	eng.mu.Lock()
	defer eng.mu.Unlock()

	source, err := eng.loadCopyLocked(execID)
	if err != nil {
		return "", err
	}
	if !source.Status.Terminal() {
		return "", fmt.Errorf("execution %q is still %s; cancel it before restarting", execID, source.Status)
	}
	selected, stageName, err := selectRestartRun(source, opts)
	if err != nil {
		return "", err
	}
	template, ok := eng.pipelines[source.Pipeline]
	if !ok {
		return "", fmt.Errorf("pipeline %q is no longer available", source.Pipeline)
	}
	p, params, err := template.WithParams(source.Params)
	if err != nil {
		return "", fmt.Errorf("pipeline %q no longer accepts this execution's params: %w", source.Pipeline, err)
	}
	node := p.Node(selected.NodeID)
	if node == nil {
		return "", fmt.Errorf("agent %q is no longer in pipeline %q", selected.NodeID, source.Pipeline)
	}
	if node.Type != selected.Type {
		return "", fmt.Errorf("agent %q changed type from %s to %s; it cannot be restarted", node.ID, selected.Type, node.Type)
	}
	stageIndex := 0
	if stageName != "" {
		if stageIndex, err = restartStageIndex(node, selected, stageName); err != nil {
			return "", err
		}
	}

	current := fingerprint(p)
	recovery := &Recovery{
		SourceExecution: source.ID,
		SourceRun:       selected.ID,
		NodeID:          node.ID,
		Stage:           stageName,
		At:              time.Now().UTC(),
	}
	if changes := definitionChanges(source.Definition, current); len(changes) > 0 {
		if !opts.Force {
			return "", &DefinitionChangedError{Pipeline: source.Pipeline, Execution: source.ID, Changes: changes}
		}
		recovery.Forced = true
		for _, change := range changes {
			recovery.Warnings = append(recovery.Warnings, "pipeline definition changed: "+change)
		}
	} else if source.Definition == nil {
		recovery.Warnings = append(recovery.Warnings, "source execution predates definition fingerprints, so pipeline changes cannot be detected")
	}

	downstream := downstreamOf(p, node.ID)
	rerun := func(nodeID string) bool { return nodeID == node.ID || downstream[nodeID] }

	ctx, cancel := context.WithCancel(context.Background())
	le := &liveExec{
		exec: &Execution{
			ID:         newExecID(),
			Pipeline:   source.Pipeline,
			Input:      source.Input,
			Params:     params,
			Status:     StatusRunning,
			Definition: current,
			Recovery:   recovery,
			CreatedAt:  time.Now().UTC(),
			Runs:       []*NodeRun{},
		},
		p:              p,
		ctx:            ctx,
		cancel:         cancel,
		done:           make(chan struct{}),
		approvals:      map[string]chan approvalDecision{},
		retries:        map[string]*pendingRetry{},
		orchs:          map[string]*orchInstance{},
		outputsChanged: make(chan struct{}),
		joinExpected:   joinCountsFrom(p, node.ID),
	}

	rootRun := &NodeRun{
		ID:                      newRunID(),
		NodeID:                  node.ID,
		Type:                    node.Type,
		Status:                  StatusPending,
		Input:                   selected.Input,
		Format:                  node.Format,
		AdditionalInputResolved: selected.AdditionalInputResolved,
		FromNodeID:              selected.FromNodeID,
		Snapshot:                selected.Snapshot,
	}
	if selected.Context != nil {
		// Reuse the saved selection so the call sees the same prior work.
		selection := *selected.Context
		selection.Entries = append([]ContextEntry(nil), selected.Context.Entries...)
		rootRun.Context = &selection
	}
	snapshot, haveSnapshot := selected.Snapshot, true
	if selected.Status == StatusInterrupted && selected.Session != nil {
		session := *selected.Session
		session.Turns = append([]SessionTurn(nil), selected.Session.Turns...)
		rootRun.Session = &session
		recovery.Resumed = true
		// The transcript is resumed, so the chunk list must match it.
		snapshot, haveSnapshot = &RunSnapshot{Chunks: source.Chunks, NextChunk: source.NextChunk}, true
	}

	// Carry completed history that the restart does not invalidate.
	carried := map[string]bool{}
	for _, run := range source.Runs {
		if run.ID == selected.ID || run.StageID != "" {
			continue
		}
		if run.Status != StatusCompleted && run.Status != StatusRejected {
			if !rerun(run.NodeID) && !completedLater(source.Runs, run) {
				recovery.Warnings = append(recovery.Warnings, fmt.Sprintf("run of %q (%s) is not downstream of the restart point and will not run again", run.NodeID, run.Status))
			}
			continue
		}
		if rerun(run.NodeID) && !finishedBefore(run, selected) {
			continue
		}
		carried[run.ID] = true
	}
	var stageIDs map[string]bool
	if stageIndex > 0 {
		stageIDs = map[string]bool{}
		for _, stage := range node.Stages[:stageIndex] {
			stageIDs[stage.ID] = true
		}
	}
	for _, run := range source.Runs {
		keep := carried[run.ID] || (run.StageID != "" && carried[run.OrchRunID])
		if !keep && stageIDs != nil && run.OrchRunID == selected.ID && stageIDs[run.StageID] {
			keep = true
			run.OrchRunID = rootRun.ID
		}
		if !keep {
			continue
		}
		if run.ReusedFrom == "" {
			run.ReusedFrom = source.ID
		}
		le.exec.Runs = append(le.exec.Runs, run)
		recovery.ReusedRuns++
	}
	for _, final := range source.FinalOutputs {
		if carried[final.NodeRunID] && !rerun(final.NodeID) {
			le.exec.FinalOutputs = append(le.exec.FinalOutputs, final)
		}
	}

	if stageIndex > 0 {
		for _, stage := range selected.Stages[:stageIndex] {
			copied := *stage
			copied.ownerRunID = rootRun.ID
			rootRun.Stages = append(rootRun.Stages, &copied)
		}
		if stageIndex < len(selected.Stages) {
			snapshot = selected.Stages[stageIndex].Snapshot
		} else {
			snapshot, haveSnapshot = nil, false
		}
		rootRun.AdditionalInputResolved = true
	}
	switch {
	case snapshot != nil:
		le.exec.Chunks = append([]Chunk(nil), snapshot.Chunks...)
		le.exec.NextChunk = snapshot.NextChunk
	case !haveSnapshot || source.Definition == nil:
		// No snapshot was taken (the stage never began, or the run predates
		// snapshots), so the latest chunk state is the best available.
		le.exec.Chunks = append([]Chunk(nil), source.Chunks...)
		le.exec.NextChunk = source.NextChunk
	}

	le.exec.Runs = append(le.exec.Runs, rootRun)
	le.liveRuns = 1
	eng.execs[le.exec.ID] = le

	// Replay handoffs from carried runs into nodes that will run again, so
	// joins wait for (and include) them.
	type seed struct{ to, input, from string }
	var seeds []seed
	for _, run := range le.exec.Runs {
		if run == rootRun || run.StageID != "" || rerun(run.NodeID) {
			continue
		}
		sourceNode := p.Node(run.NodeID)
		if sourceNode == nil {
			continue
		}
		for _, route := range selectHandoffs(sourceNode, runOutcome(run)) {
			if !downstream[route.To] || route.To == node.ID {
				continue
			}
			input := run.Output
			if route.Input != "" {
				input = renderHandoffTemplate(route.Input, run.Input, run.Output, run.Comment)
			}
			le.joinExpected[route.To]++
			seeds = append(seeds, seed{to: route.To, input: input, from: run.NodeID})
		}
	}
	for _, s := range seeds {
		eng.deliverLocked(le, s.to, s.input, s.from, nil)
	}

	if node.Type == pipeline.TypeOrchestrator {
		ready := make(chan struct{})
		close(ready)
		orchestrator := &orchInstance{
			node:        node,
			run:         rootRun,
			inputs:      []deliveredInput{{from: selected.FromNodeID, input: selected.Input}},
			inputsReady: ready,
			readyClosed: true,
			startStage:  stageIndex,
		}
		if stageIndex > 0 {
			orchestrator.resumeContext = selected.Stages[stageIndex-1].Context
		}
		le.orchs[node.ID] = orchestrator
		go eng.runOrchestrator(le, orchestrator)
	} else {
		go eng.runNode(le, rootRun, node, nil)
	}
	eng.persistLocked(le)
	return le.exec.ID, nil
}

// loadCopyLocked returns an independent copy of a live or stored execution.
func (eng *Engine) loadCopyLocked(execID string) (*Execution, error) {
	if le, ok := eng.execs[execID]; ok {
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
	if eng.store == nil {
		return nil, fmt.Errorf("unknown execution %q", execID)
	}
	return eng.store.Get(execID)
}

func selectRestartRun(source *Execution, opts RestartOptions) (*NodeRun, string, error) {
	runID, stage := opts.RunID, opts.Stage
	if runID == "" && opts.NodeID != "" {
		for i := len(source.Runs) - 1; i >= 0; i-- {
			if run := source.Runs[i]; run.NodeID == opts.NodeID && run.StageID == "" {
				runID = run.ID
				break
			}
		}
		if runID == "" {
			return nil, "", fmt.Errorf("agent %q has no top-level run in execution %q", opts.NodeID, source.ID)
		}
	}
	if runID == "" {
		var err error
		if runID, stage, err = RestartPoint(source); err != nil {
			return nil, "", err
		}
		if opts.Stage != "" {
			stage = opts.Stage
		}
	}
	for _, run := range source.Runs {
		if run.ID != runID {
			continue
		}
		if run.StageID != "" {
			return nil, "", fmt.Errorf("run %q is inside stage %q of an orchestrator; restart orchestrator run %q from that stage instead", runID, run.StageID, run.OrchRunID)
		}
		if run.ReusedFrom != "" {
			return nil, "", fmt.Errorf("run %q was reused from execution %s; restart from that execution instead", runID, run.ReusedFrom)
		}
		if stage != "" && run.Type != pipeline.TypeOrchestrator {
			return nil, "", fmt.Errorf("run %q is not an orchestrator, so it has no stages", runID)
		}
		return run, stage, nil
	}
	return nil, "", fmt.Errorf("unknown run %q in execution %q", runID, source.ID)
}

// restartStageIndex validates restarting an orchestrator run from a stage:
// every earlier stage must have completed, under the same stage IDs.
func restartStageIndex(node *pipeline.Node, run *NodeRun, stageName string) (int, error) {
	index := -1
	for i, stage := range node.Stages {
		if stage.ID == stageName {
			index = i
			break
		}
	}
	if index < 0 {
		return 0, fmt.Errorf("orchestrator %q has no stage %q", node.ID, stageName)
	}
	for i := 0; i < index; i++ {
		if i >= len(run.Stages) || run.Stages[i].StageID != node.Stages[i].ID || run.Stages[i].Status != StatusCompleted {
			return 0, fmt.Errorf("stage %q cannot be restarted: earlier stage %q did not complete in this run", stageName, node.Stages[i].ID)
		}
	}
	return index, nil
}

// downstreamOf returns every node reachable through handoffs from root,
// excluding root unless it is part of a cycle.
func downstreamOf(p *pipeline.Pipeline, root string) map[string]bool {
	reached := map[string]bool{}
	queue := append([]string{}, p.Node(root).HandoffTargets()...)
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		if reached[id] {
			continue
		}
		reached[id] = true
		if n := p.Node(id); n != nil {
			queue = append(queue, n.HandoffTargets()...)
		}
	}
	return reached
}

// finishedBefore reports whether run finished before the restart point
// started; such runs are history the restart point already saw.
func finishedBefore(run, point *NodeRun) bool {
	return run.FinishedAt != nil && point.StartedAt != nil && run.FinishedAt.Before(*point.StartedAt)
}

// completedLater reports whether a later top-level run of the same node
// completed, superseding an unfinished one (for example after rework).
func completedLater(runs []*NodeRun, run *NodeRun) bool {
	after := false
	for _, other := range runs {
		if other == run {
			after = true
			continue
		}
		if after && other.NodeID == run.NodeID && other.StageID == "" && other.Status == StatusCompleted {
			return true
		}
	}
	return false
}

// runOutcome recovers the routing outcome a finished run handed off with.
func runOutcome(run *NodeRun) string {
	if run.Decision != "" {
		return run.Decision
	}
	if run.Status == StatusRejected {
		return "reject"
	}
	return ""
}
