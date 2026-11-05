package pipeline

import (
	"fmt"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// MaxMCPServersPerAgent bounds the MCP servers one agent may request.
const MaxMCPServersPerAgent = 10

// MCPUse is an agent's opt-in to one MCP server registered in
// signalbox.yaml. Tools narrows the server's allow-list; empty means every
// tool the server entry allows. A YAML scalar is shorthand for the server name.
type MCPUse struct {
	Server string   `yaml:"server" json:"server"`
	Tools  []string `yaml:"tools,omitempty" json:"tools,omitempty"`
}

func (m *MCPUse) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind == yaml.ScalarNode {
		*m = MCPUse{Server: node.Value}
		return nil
	}
	type plain MCPUse
	var use plain
	if err := node.Decode(&use); err != nil {
		return err
	}
	*m = MCPUse(use)
	return nil
}

var (
	// MCPNamePattern restricts MCP server and tool names to characters that
	// are safe in every agent CLI's configuration syntax.
	MCPNamePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	MCPToolPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,128}$`)
)

// validateIntegrations checks the model override and MCP opt-in fields.
// Whether the chosen runtime supports them is checked against the project
// config by runtime.ValidatePipeline.
func validateIntegrations(n *Node) error {
	if n.Model != "" {
		switch n.Type {
		case TypeStandard, TypeLLMGate, TypeOrchestrator:
		default:
			return fmt.Errorf("agent %q: model is only supported on standard, llm_gate, and orchestrator nodes", n.ID)
		}
		literal := stripParamRefs(n.Model)
		if strings.HasPrefix(n.Model, "-") || strings.ContainsAny(literal, " \t\r\n") {
			return fmt.Errorf("agent %q: model %q must not start with '-' or contain whitespace", n.ID, n.Model)
		}
	}
	if len(n.MCP) == 0 {
		return nil
	}
	if n.Type != TypeStandard && n.Type != TypeLLMGate {
		return fmt.Errorf("agent %q: mcp is only supported on standard and llm_gate nodes", n.ID)
	}
	if len(n.MCP) > MaxMCPServersPerAgent {
		return fmt.Errorf("agent %q: at most %d mcp servers are allowed", n.ID, MaxMCPServersPerAgent)
	}
	servers := map[string]bool{}
	for _, use := range n.MCP {
		if !MCPNamePattern.MatchString(use.Server) {
			return fmt.Errorf("agent %q: mcp server name %q must be 1-64 letters, digits, '_' or '-'", n.ID, use.Server)
		}
		if servers[use.Server] {
			return fmt.Errorf("agent %q lists mcp server %q more than once", n.ID, use.Server)
		}
		servers[use.Server] = true
		tools := map[string]bool{}
		for _, tool := range use.Tools {
			if !MCPToolPattern.MatchString(tool) {
				return fmt.Errorf("agent %q: mcp server %q tool %q must be 1-128 letters, digits, '_', '.' or '-' (wildcards are not allowed)", n.ID, use.Server, tool)
			}
			if tools[tool] {
				return fmt.Errorf("agent %q: mcp server %q lists tool %q more than once", n.ID, use.Server, tool)
			}
			tools[tool] = true
		}
	}
	return nil
}
