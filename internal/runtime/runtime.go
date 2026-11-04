// Package runtime abstracts the AI backends an agent can execute against:
// the OpenAI-compatible HTTP API, local agent CLIs (codex, claude, copilot,
// or any custom command), and a deterministic mock for tests and demos.
package runtime

import (
	"context"
	"fmt"

	"github.com/gitea/signalbox/internal/config"
)

// Request is a single agent invocation.
type Request struct {
	// SystemPrompt is the agent's prompt from the pipeline YAML.
	SystemPrompt string
	// Input is the handoff payload (initial input or upstream output).
	Input string
}

// Runtime executes one request and returns the agent's output text.
type Runtime interface {
	Name() string
	Invoke(ctx context.Context, req Request) (string, error)
}

// Registry maps runtime names from signalbox.yaml to implementations.
type Registry struct {
	runtimes map[string]Runtime
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

// Register overrides or adds a runtime (used by tests).
func (r *Registry) Register(rt Runtime) {
	r.runtimes[rt.Name()] = rt
}

func build(name string, rc config.RuntimeConfig) (Runtime, error) {
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
