package engine

import (
	"fmt"
	"strings"
	"time"

	"github.com/kaladinstorm84/SignalBox/internal/pipeline"
	"github.com/kaladinstorm84/SignalBox/internal/runtime"
)

func (eng *Engine) invokeAgent(le *liveExec, run *NodeRun, agent *pipeline.Node, policy *pipeline.RetryPolicy, retry int, req runtime.Request) (string, error) {
	name := agent.Runtime
	req.ReasoningEffort, req.Speed = agent.ReasoningEffort, agent.Speed
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
	out, err := rt.Invoke(le.ctx, req)
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
