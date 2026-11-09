package runtime

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/kaladinstorm84/SignalBox/internal/config"
)

// cliRuntime shells out to a local agent CLI (codex, claude, copilot, or any
// custom command). The system prompt and handoff input are combined into a
// single prompt, delivered as the final argument or via stdin.
type cliRuntime struct {
	name       string
	command    string
	args       []string
	promptMode string // "arg" or "stdin"
	stdinArgs  []string
	timeout    time.Duration
	dir        string
	// kind is the runtime type (codex, claude, copilot, or cli).
	kind string
	// modelArg is the flag that receives a per-agent model override.
	modelArg string
	model    string
	options  config.RuntimeConfig
	// mcpServers are the servers registered in signalbox.yaml.
	mcpServers map[string]config.MCPServerConfig
	// code marks a code node: its stdin is the raw input, so file paths are
	// only passed through the environment.
	code bool
}

// filesInstructions tells an agent CLI where the uploaded files are.
func filesInstructions(req Request) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Attached files (read-only copies in %s):", req.FilesDir)
	for _, path := range req.Files {
		b.WriteString("\n- " + path)
	}
	return b.String()
}

// filesArgs exposes the files folder to the child process: always through
// SIGNALBOX_FILES_DIR, and to claude/copilot agents with --add-dir so their
// sandboxes allow reading it.
func (c *cliRuntime) filesArgs(req Request) (args, env []string) {
	if req.FilesDir == "" {
		return nil, nil
	}
	env = []string{FilesDirEnv + "=" + req.FilesDir}
	if (c.kind == "claude" || c.kind == "copilot") && !c.code {
		args = []string{"--add-dir", req.FilesDir}
	}
	return args, env
}

// maxArgPrompt keeps arg-mode prompts well inside the 32,767-character
// Windows command-line limit, leaving room for the command, other arguments
// and quoting.
const maxArgPrompt = 24000

type ProcessExitError struct {
	ExitCode int
	Stdout   string
	Stderr   string
	cause    error
	message  string
}

func (e *ProcessExitError) Error() string { return e.message }
func (e *ProcessExitError) Unwrap() error { return e.cause }

// Presets for the known agent CLIs. All of them support a non-interactive
// single-prompt mode; the user can override command/args in signalbox.yaml.
// stdinArgs, when set, are appended instead of the prompt to make the CLI read
// the prompt from stdin; prompts too long for the command line use this.
var cliPresets = map[string]struct {
	command   string
	args      []string
	stdinArgs []string
}{
	"codex":   {command: "codex", args: []string{"exec"}, stdinArgs: []string{"-"}},
	"claude":  {command: "claude", args: []string{"-p"}, stdinArgs: []string{}},
	"copilot": {command: "copilot", args: []string{"-p"}},
}

// presetModelArg is the model flag understood by each preset CLI.
const presetModelArg = "--model"

func newCLI(name string, rc config.RuntimeConfig) (Runtime, error) {
	command := rc.Command
	args := rc.Args
	var stdinArgs []string
	modelArg := rc.ModelArg
	if preset, ok := cliPresets[rc.Type]; ok {
		modelArg = presetModelArg
		if command == "" {
			command = preset.command
		}
		if args == nil {
			args = preset.args
		}
		stdinArgs = preset.stdinArgs
	}
	if command == "" {
		return nil, fmt.Errorf("cli runtime requires a command")
	}
	mode := rc.PromptMode
	if mode == "" {
		mode = "arg"
	}
	if mode != "arg" && mode != "stdin" {
		return nil, fmt.Errorf("prompt_mode must be \"arg\" or \"stdin\", got %q", mode)
	}
	timeout := time.Duration(rc.TimeoutSeconds) * time.Second
	if timeout == 0 {
		timeout = 10 * time.Minute
	}
	return &cliRuntime{
		name:       name,
		command:    command,
		args:       args,
		promptMode: mode,
		stdinArgs:  stdinArgs,
		timeout:    timeout,
		kind:       rc.Type,
		modelArg:   modelArg,
		model:      rc.Model,
		options:    rc,
	}, nil
}

func (c *cliRuntime) Name() string { return c.name }

func (c *cliRuntime) Invoke(ctx context.Context, req Request) (string, error) {
	prompt := req.Input
	if req.SystemPrompt != "" {
		prompt = req.SystemPrompt + "\n\n---\n\n" + req.Input
	}
	if req.Schema != nil {
		prompt += "\n\n---\n\n" + req.Schema.Instructions()
	}
	if req.FilesDir != "" && !c.code {
		prompt += "\n\n---\n\n" + filesInstructions(req)
	}

	extra, env, cleanup, err := c.integrationArgs(req)
	if err != nil {
		return "", err
	}
	defer cleanup()
	filesExtra, filesEnv := c.filesArgs(req)
	extra = append(extra, filesExtra...)
	env = append(env, filesEnv...)

	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	args := c.baseArgs(extra)
	var stdin *bytes.Reader
	switch {
	case c.promptMode == "stdin":
		stdin = bytes.NewReader([]byte(prompt))
	case argLength(prompt) <= maxArgPrompt:
		args = append(args, prompt)
	case c.stdinArgs != nil:
		args = append(args, c.stdinArgs...)
		stdin = bytes.NewReader([]byte(prompt))
	default:
		return "", fmt.Errorf("prompt is %d characters, too long to pass to %s as a command-line argument; set prompt_mode: stdin for runtime %q if the command can read its prompt from stdin", len(prompt), c.command, c.name)
	}

	cmd := exec.CommandContext(ctx, c.command, args...)
	configureProcess(cmd)
	cmd.Dir = c.dir
	if len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
	}
	if stdin != nil {
		cmd.Stdin = stdin
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return "", fmt.Errorf("%s timed out after %s", c.command, c.timeout)
		}
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = strings.TrimSpace(stdout.String())
		}
		failure := fmt.Errorf("%s failed: %w: %s", c.command, err, truncate(detail, 800))
		var exitError *exec.ExitError
		if ctx.Err() == nil && errors.As(err, &exitError) {
			return "", &ProcessExitError{
				ExitCode: exitError.ExitCode(), Stdout: strings.TrimSpace(stdout.String()), Stderr: strings.TrimSpace(stderr.String()),
				cause: err, message: failure.Error(),
			}
		}
		return "", failure
	}
	return strings.TrimSpace(stdout.String()), nil
}

// argLength estimates the command-line length of an argument, counting the
// escaping Windows applies to quotes and backslashes.
func argLength(s string) int {
	return len(s) + strings.Count(s, `"`) + strings.Count(s, `\`) + 2
}
