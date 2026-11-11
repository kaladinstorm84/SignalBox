package engine

import (
	"errors"
	"fmt"
	"strings"

	"github.com/kaladinstorm84/SignalBox/internal/pipeline"
)

// childRejectedError reports a child execution that ended rejected. The
// pipeline node is rejected in turn rather than parked for retry.
type childRejectedError struct {
	execution string
	reason    string
}

func (e *childRejectedError) Error() string {
	return fmt.Sprintf("child execution %s was rejected: %s", e.execution, e.reason)
}

// runPipelineNode runs another pipeline (or a section of it) as a linked
// child execution and hands off its final output. A failed or canceled
// child is handled like a failed agent call: retried automatically per the
// node's retry policy, then parked in waiting_retry, where a retry starts a
// fresh child. A rejected child rejects the node.
func (eng *Engine) runPipelineNode(le *liveExec, run *NodeRun, node *pipeline.Node, octx *stageState) {
	out, err := eng.invokeWithRetries(le, run, node.Retry, false, func(_ int) (string, error) {
		return eng.runChild(le, run, node)
	})
	if err != nil {
		var rejected *childRejectedError
		if errors.As(err, &rejected) {
			eng.finishRunRejected(le, run, node, octx, rejected.reason, run.Input)
			return
		}
		eng.finishRunErr(le, run, node, octx, err)
		return
	}
	eng.finishRunOK(le, run, node, out, octx, "")
}

// runChild starts one child execution and waits for it to finish. If the
// parent ends first, the child is canceled.
func (eng *Engine) runChild(le *liveExec, run *NodeRun, node *pipeline.Node) (string, error) {
	eng.mu.Lock()
	if err := le.ctx.Err(); err != nil {
		eng.mu.Unlock()
		return "", err
	}
	depth := 1
	if le.exec.Parent != nil {
		depth = le.exec.Parent.Depth + 1
	}
	if depth > pipeline.MaxPipelineDepth {
		eng.mu.Unlock()
		return "", fmt.Errorf("pipeline %q would nest deeper than %d child executions", node.Pipeline, pipeline.MaxPipelineDepth)
	}
	spec := startSpec{
		pipeline: node.Pipeline,
		input:    run.Input,
		params:   node.Params,
		from:     node.From,
		until:    node.Until,
		parent:   &ParentLink{Execution: le.exec.ID, RunID: run.ID, NodeID: node.ID, Depth: depth},
	}
	if node.FilesMode() == pipeline.FilesInherit {
		spec.reuse = &reusedFiles{exec: le.exec.ID, files: le.exec.Files}
		run.Files = &RunFiles{Mode: pipeline.FilesInherit}
		for _, f := range le.exec.Files {
			run.Files.Names = append(run.Files.Names, f.Name)
		}
	}
	child, err := eng.startLocked(spec)
	if err != nil {
		eng.mu.Unlock()
		return "", fmt.Errorf("starting pipeline %q: %w", node.Pipeline, err)
	}
	run.ChildExecutions = append(run.ChildExecutions, child.exec.ID)
	if le.children == nil {
		le.children = map[string]*liveExec{}
	}
	le.children[run.ID] = child
	eng.persistLocked(le)
	eng.mu.Unlock()

	select {
	case <-child.done:
	case <-le.ctx.Done():
		eng.mu.Lock()
		delete(le.children, run.ID)
		if !child.exec.Status.Terminal() {
			child.exec.Status = StatusCanceled
			child.exec.Error = fmt.Sprintf("parent execution %s ended", le.exec.ID)
			child.cancel()
			eng.persistLocked(child)
		}
		eng.mu.Unlock()
		return "", le.ctx.Err()
	}

	eng.mu.Lock()
	defer eng.mu.Unlock()
	delete(le.children, run.ID)
	switch child.exec.Status {
	case StatusCompleted:
		return childOutput(child.exec.FinalOutputs), nil
	case StatusRejected:
		reason := child.exec.Error
		if reason == "" {
			reason = "rejected"
		}
		return "", &childRejectedError{execution: child.exec.ID, reason: reason}
	}
	reason := child.exec.Error
	if reason == "" {
		reason = string(child.exec.Status)
	}
	return "", fmt.Errorf("child execution %s (pipeline %q) %s: %s", child.exec.ID, node.Pipeline, child.exec.Status, reason)
}

// childOutput is the result a pipeline node hands off: the child's single
// final output, or every final output labelled by agent.
func childOutput(finals []FinalOutput) string {
	if len(finals) == 1 {
		return finals[0].Output
	}
	var b strings.Builder
	for i, f := range finals {
		if i > 0 {
			b.WriteString("\n\n")
		}
		fmt.Fprintf(&b, "--- result from %s ---\n%s", f.NodeID, f.Output)
	}
	return b.String()
}

// cancelChildrenLocked cancels the live child executions of le. Child
// executions also notice their parent ending through runChild; this makes
// the cancellation immediate and visible before Cancel returns.
func (eng *Engine) cancelChildrenLocked(le *liveExec, reason string) {
	for _, child := range le.children {
		if child.exec.Status.Terminal() {
			continue
		}
		child.exec.Status = StatusCanceled
		child.exec.Error = reason
		if child.cancel != nil {
			child.cancel()
		}
		eng.cancelChildrenLocked(child, reason)
		eng.persistLocked(child)
	}
}
