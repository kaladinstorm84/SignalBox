package runtime

import (
	"fmt"
	"slices"

	"github.com/kaladinstorm84/SignalBox/internal/config"
)

// CLICommand is a resolved invocation without a prompt or environment values.
type CLICommand struct {
	Type           string   `json:"type"`
	Command        string   `json:"command"`
	Args           []string `json:"args"`
	PromptMode     string   `json:"prompt_mode"`
	StdinArgs      []string `json:"stdin_args"`
	ArgLimit       int      `json:"arg_limit"`
	TimeoutSeconds float64  `json:"timeout_seconds"`
}

// ExportCLICommand uses the same presets, call settings and argument ordering
// as a live call. Integration grants and files must be handled by the caller.
func ExportCLICommand(name string, rc config.RuntimeConfig, req Request) (CLICommand, error) {
	switch rc.Type {
	case "codex", "claude", "copilot", "cli":
	default:
		return CLICommand{}, fmt.Errorf("Python export does not support runtime %q (type %s); use a CLI runtime", name, rc.Type)
	}
	if len(req.MCP) > 0 || req.Schema != nil || req.FilesDir != "" || len(req.Files) > 0 {
		return CLICommand{}, fmt.Errorf("Python export does not support MCP grants, schemas, or uploaded files")
	}
	rt, err := newCLI(name, rc)
	if err != nil {
		return CLICommand{}, err
	}
	c := rt.(*cliRuntime)
	extra, _, cleanup, err := c.integrationArgs(req)
	defer cleanup()
	if err != nil {
		return CLICommand{}, err
	}
	timeout, err := invocationTimeout(c.timeout, req.TimeoutSeconds)
	if err != nil {
		return CLICommand{}, err
	}
	args := c.baseArgs(extra)
	if c.kind == "codex" && !slices.Contains(args, "--json") {
		args = append(args, "--json")
	}
	return CLICommand{
		Type: c.kind, Command: c.command, Args: args,
		PromptMode: c.promptMode, StdinArgs: c.stdinArgs,
		ArgLimit: maxArgPrompt, TimeoutSeconds: timeout.Seconds(),
	}, nil
}
