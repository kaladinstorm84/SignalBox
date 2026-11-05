package engine

import (
	"github.com/kaladinstorm84/SignalBox/internal/pipeline"
	"github.com/kaladinstorm84/SignalBox/internal/runtime"
)

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
