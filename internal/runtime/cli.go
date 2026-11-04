package runtime

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/gitea/signalbox/internal/config"
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
}

// maxArgPrompt keeps arg-mode prompts well inside the 32,767-character
// Windows command-line limit, leaving room for the command, other arguments
// and quoting.
const maxArgPrompt = 24000

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

func newCLI(name string, rc config.RuntimeConfig) (Runtime, error) {
	command := rc.Command
	args := rc.Args
	var stdinArgs []string
	if preset, ok := cliPresets[rc.Type]; ok {
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
	}, nil
}

func (c *cliRuntime) Name() string { return c.name }

func (c *cliRuntime) Invoke(ctx context.Context, req Request) (string, error) {
	prompt := req.Input
	if req.SystemPrompt != "" {
		prompt = req.SystemPrompt + "\n\n---\n\n" + req.Input
	}

	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	args := append([]string{}, c.args...)
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
		return "", fmt.Errorf("%s failed: %w: %s", c.command, err, truncate(detail, 800))
	}
	return strings.TrimSpace(stdout.String()), nil
}

// argLength estimates the command-line length of an argument, counting the
// escaping Windows applies to quotes and backslashes.
func argLength(s string) int {
	return len(s) + strings.Count(s, `"`) + strings.Count(s, `\`) + 2
}
