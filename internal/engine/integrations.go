package engine

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/kaladinstorm84/SignalBox/internal/pipeline"
	"github.com/kaladinstorm84/SignalBox/internal/runtime"
)

func (eng *Engine) invokeAgent(le *liveExec, run *NodeRun, agent *pipeline.Node, policy *pipeline.RetryPolicy, retry int, req runtime.Request, stageTarget ...*StageRun) (string, error) {
	name := agent.Runtime
	req.ReasoningEffort, req.Speed = agent.ReasoningEffort, agent.Speed
	req.TimeoutSeconds = agent.TimeoutSeconds
	if policy != nil && retry > 0 && len(policy.Attempts) > 0 {
		choice := policy.Attempts[retry-1]
		if choice.Runtime != "" {
			name = choice.Runtime
		}
		if choice.Model != "" {
			req.Model = choice.Model
		}
		if choice.ReasoningEffort != "" {
			req.ReasoningEffort = choice.ReasoningEffort
		}
		if choice.Speed != "" {
			req.Speed = choice.Speed
		}
	}
	rt, req, err := eng.registry.Resolve(name, req)
	if err != nil {
		return "", err
	}
	eng.mu.Lock()
	index := len(run.CallAttempts)
	run.CallAttempts = append(run.CallAttempts, CallAttempt{
		Agent: agent.ID, Retry: retry, Runtime: name, Model: req.Model,
		ReasoningEffort: req.ReasoningEffort, Speed: req.Speed, StartedAt: time.Now().UTC(),
	})
	eng.persistLocked(le)
	eng.mu.Unlock()
	var target *StageRun
	if len(stageTarget) > 0 {
		target = stageTarget[0]
	}
	onOutput, flush := eng.streamOutputLogger(le, run, target, index+1)
	var out string
	if streaming, ok := rt.(runtime.StreamingRuntime); ok {
		out, err = streaming.InvokeStream(le.ctx, req, onOutput)
	} else {
		out, err = rt.Invoke(le.ctx, req)
		if err == nil && onOutput != nil {
			onOutput(out)
		}
	}
	if flush != nil {
		flush()
	}
	eng.mu.Lock()
	now := time.Now().UTC()
	run.CallAttempts[index].FinishedAt = &now
	if err != nil {
		run.CallAttempts[index].Error = err.Error()
	}
	eng.persistLocked(le)
	eng.mu.Unlock()
	return out, err
}

func (eng *Engine) streamOutputLogger(le *liveExec, run *NodeRun, target *StageRun, attempt int) (func(string), func()) {
	var callbackMu sync.Mutex
	var pending strings.Builder
	var lastPersist time.Time
	write := func(text string, force bool) {
		callbackMu.Lock()
		defer callbackMu.Unlock()
		if text != "" {
			pending.WriteString(text)
		}
		if pending.Len() == 0 || (!force && pending.Len() < 4096 && time.Since(lastPersist) < time.Second) {
			return
		}
		chunk := pending.String()
		pending.Reset()
		eng.mu.Lock()
		defer eng.mu.Unlock()

		stage := target
		if stage == nil {
			for _, orchestrator := range le.exec.Runs {
				if orchestrator.ID != run.OrchRunID {
					continue
				}
				for _, candidate := range orchestrator.Stages {
					if candidate.StageID == run.StageID {
						stage = candidate
						break
					}
				}
				break
			}
		}
		now := time.Now().UTC()
		logs := &run.Logs
		if stage != nil {
			logs = &stage.Logs
		}
		var entry *StageLog
		for i := range *logs {
			if (*logs)[i].RunID == run.ID && (*logs)[i].Attempt == attempt {
				entry = &(*logs)[i]
				break
			}
		}
		if entry == nil {
			*logs = append(*logs, StageLog{
				RunID: run.ID, NodeID: run.NodeID, Attempt: attempt, UpdatedAt: now,
			})
			entry = &(*logs)[len(*logs)-1]
		}
		entry.Text += chunk
		entry.UpdatedAt = now
		eng.persistLocked(le)
		lastPersist = time.Now()
	}
	stop := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				write("", true)
			case <-stop:
				return
			}
		}
	}()
	return func(text string) { write(text, false) }, func() {
		close(stop)
		<-stopped
		write("", true)
	}
}

// withKnowledge reads current documents for a call without rewriting the
// persisted handoff payload. Reference text is appended only to runtime input.
func (eng *Engine) withKnowledge(input string, names []string) (string, error) {
	if len(names) == 0 {
		return input, nil
	}
	if eng.knowledge == nil {
		return "", fmt.Errorf("knowledge_bases requires a project root")
	}
	content, err := eng.knowledge.Content(names)
	if err != nil {
		return "", err
	}
	if content == "" {
		return input, nil
	}
	return strings.TrimRight(input, "\n") + content, nil
}

func mergeKnowledgeBases(groups ...[]string) []string {
	seen := map[string]bool{}
	var names []string
	for _, group := range groups {
		for _, name := range group {
			if !seen[name] {
				seen[name] = true
				names = append(names, name)
			}
		}
	}
	return names
}

// recordIntegrationsLocked records the model override and MCP grants a run
// will use. Grants are resolved against the current registry each time the
// run starts, so a restart picks up configuration changes.
func (eng *Engine) recordIntegrationsLocked(le *liveExec, run *NodeRun, node *pipeline.Node) error {
	if node.Type == pipeline.TypeSession {
		var grants []runtime.MCPGrant
		seen := map[string]bool{}
		for _, id := range append(append([]string{}, node.Participants...), node.SummaryAgent) {
			agent := le.p.Node(id)
			if id == "" || seen[id] || agent == nil {
				continue
			}
			seen[id] = true
			granted, err := eng.registry.GrantMCP(id, agent.MCP)
			if err != nil {
				return err
			}
			grants = append(grants, granted...)
		}
		run.MCP = grants
		return nil
	}
	run.Model = node.Model
	grants, err := eng.registry.GrantMCP(node.ID, node.MCP)
	if err != nil {
		return err
	}
	run.MCP = grants
	return nil
}

// agentRequest builds a runtime request carrying the run's model and grants.
func agentRequest(run *NodeRun, prompt, input string) runtime.Request {
	return runtime.Request{SystemPrompt: prompt, Input: input, Model: run.Model, MCP: run.MCP}
}

// agentGrants returns the grants recorded for one agent of a session.
func agentGrants(grants []runtime.MCPGrant, agent string) []runtime.MCPGrant {
	var out []runtime.MCPGrant
	for _, g := range grants {
		if g.Agent == agent {
			out = append(out, g)
		}
	}
	return out
}
