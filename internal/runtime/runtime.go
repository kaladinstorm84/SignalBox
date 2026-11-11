// Package runtime abstracts the AI backends an agent can execute against:
// the OpenAI-compatible HTTP API, local agent CLIs (codex, claude, copilot,
// or any custom command), and a deterministic mock for tests and demos.
package runtime

import (
	"context"
	"fmt"

	"github.com/kaladinstorm84/SignalBox/internal/config"
	"github.com/kaladinstorm84/SignalBox/internal/pipeline"
	"github.com/kaladinstorm84/SignalBox/internal/schema"
)

// Request is a single agent invocation.
type Request struct {
	// SystemPrompt is the agent's prompt from the pipeline YAML.
	SystemPrompt string
	// Input is the handoff payload (initial input or upstream output).
	Input string
	// Model overrides the runtime's default model when set.
	Model           string
	ReasoningEffort string
	Speed           string
	// MCP lists the MCP servers and tools granted to this call.
	MCP []MCPGrant
	// Schema, when set, is the JSON Schema the response must conform to.
	// OpenAI runtimes request it natively; CLI runtimes receive it as prompt
	// instructions; the mock returns a conforming sample.
	Schema *schema.Schema
	// FilesDir, when set, is a per-run folder holding copies of the
	// execution's uploaded files (files: path). Files lists their absolute
	// paths. CLI runtimes are told about them in the prompt; code nodes and
	// CLIs receive the folder as SIGNALBOX_FILES_DIR.
	FilesDir string
	Files    []string
}

// FilesDirEnv names the environment variable that carries Request.FilesDir.
const FilesDirEnv = "SIGNALBOX_FILES_DIR"

// MCPGrant records one MCP server and the tools an agent was allowed to use.
// It never contains environment values.
type MCPGrant struct {
	Agent  string   `json:"agent,omitempty"`
	Server string   `json:"server"`
	Tools  []string `json:"tools"`
}

// Runtime executes one request and returns the agent's output text.
type Runtime interface {
	Name() string
	Invoke(ctx context.Context, req Request) (string, error)
}

// Registry maps runtime names from signalbox.yaml to implementations.
type Registry struct {
	runtimes map[string]Runtime
	mcp      config.MCPConfig
}

// NewRegistryForConfig builds every runtime and registers the project's MCP
// servers.
func NewRegistryForConfig(cfg *config.Config) (*Registry, error) {
	r, err := NewRegistry(cfg.Runtimes)
	if err != nil {
		return nil, err
	}
	r.SetMCP(cfg.MCP)
	return r, nil
}

// SetMCP registers the MCP servers agents may be granted.
func (r *Registry) SetMCP(mcp config.MCPConfig) {
	r.mcp = mcp
	for _, rt := range r.runtimes {
		if c, ok := rt.(*cliRuntime); ok {
			c.mcpServers = mcp.Servers
		}
	}
}

// GrantMCP resolves an agent's MCP opt-in against the registered servers.
func (r *Registry) GrantMCP(agent string, uses []pipeline.MCPUse) ([]MCPGrant, error) {
	return grantMCP(r.mcp, agent, uses)
}

// grantMCP narrows a server's configured allowlist. An omitted per-agent tool
// list inherits that allowlist; it does not mean unrestricted server access.
func grantMCP(mcp config.MCPConfig, agent string, uses []pipeline.MCPUse) ([]MCPGrant, error) {
	if len(uses) == 0 {
		return nil, nil
	}
	if !mcp.Enabled {
		return nil, fmt.Errorf("agent %q requests MCP servers but mcp.enabled is not true in %s", agent, config.FileName)
	}
	grants := make([]MCPGrant, 0, len(uses))
	for _, use := range uses {
		server, ok := mcp.Servers[use.Server]
		if !ok {
			return nil, fmt.Errorf("agent %q requests MCP server %q which is not registered under mcp.servers in %s", agent, use.Server, config.FileName)
		}
		tools := use.Tools
		if len(tools) == 0 {
			tools = server.Tools
		}
		for _, tool := range tools {
			if !contains(server.Tools, tool) {
				return nil, fmt.Errorf("agent %q requests tool %q which MCP server %q does not allow", agent, tool, use.Server)
			}
		}
		grants = append(grants, MCPGrant{Agent: agent, Server: use.Server, Tools: append([]string(nil), tools...)})
	}
	return grants, nil
}

func contains(list []string, value string) bool {
	for _, item := range list {
		if item == value {
			return true
		}
	}
	return false
}

// NewRegistry builds every runtime declared in the config.
func NewRegistry(cfgs map[string]config.RuntimeConfig) (*Registry, error) {
	r := &Registry{runtimes: map[string]Runtime{}}
	for name, rc := range cfgs {
		rt, err := build(name, rc)
		if err != nil {
			return nil, fmt.Errorf("runtime %q: %w", name, err)
		}
		r.runtimes[name] = rt
	}
	return r, nil
}

// Get returns the named runtime.
func (r *Registry) Get(name string) (Runtime, error) {
	rt, ok := r.runtimes[name]
	if !ok {
		return nil, fmt.Errorf("runtime %q is not defined in %s", name, config.FileName)
	}
	return rt, nil
}

// Resolve fills call defaults without mutating a shared runtime.
func (r *Registry) Resolve(name string, req Request) (Runtime, Request, error) {
	rt, err := r.Get(name)
	if err != nil {
		return nil, req, err
	}
	var options config.RuntimeConfig
	switch typed := rt.(type) {
	case *cliRuntime:
		options = typed.options
	case *openAI:
		options = typed.options
	case *Mock:
		options.Type = "mock"
	default:
		return nil, req, fmt.Errorf("runtime %q does not expose call settings", name)
	}
	options, err = requestOptions(options, req)
	if err != nil {
		return nil, req, fmt.Errorf("runtime %q: %w", name, err)
	}
	req.Model, req.ReasoningEffort, req.Speed = options.Model, options.ReasoningEffort, options.Speed
	return rt, req, nil
}

func requestOptions(options config.RuntimeConfig, req Request) (config.RuntimeConfig, error) {
	if req.Model != "" {
		options.Model = req.Model
	}
	if req.ReasoningEffort != "" {
		options.ReasoningEffort = req.ReasoningEffort
	}
	if req.Speed != "" {
		options.Speed = req.Speed
	}
	return options, config.ValidateRuntimeOptions(options)
}

// Register overrides or adds a runtime (used by tests).
func (r *Registry) Register(rt Runtime) {
	r.runtimes[rt.Name()] = rt
}

func build(name string, rc config.RuntimeConfig) (Runtime, error) {
	if err := config.ValidateRuntimeOptions(rc); err != nil {
		return nil, err
	}
	switch rc.Type {
	case "openai":
		return newOpenAI(name, rc)
	case "codex", "claude", "copilot", "cli":
		return newCLI(name, rc)
	case "mock":
		return &Mock{RuntimeName: name}, nil
	default:
		return nil, fmt.Errorf("unknown type %q", rc.Type)
	}
}
