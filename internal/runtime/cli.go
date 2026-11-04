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
	timeout    time.Duration
}

// Presets for the known agent CLIs. All of them support a non-interactive
// single-prompt mode; the user can override command/args in signalbox.yaml.
var cliPresets = map[string]struct {
	command string
	args    []string
}{
	"codex":   {command: "codex", args: []string{"exec"}},
	"claude":  {command: "claude", args: []string{"-p"}},
	"copilot": {command: "copilot", args: []string{"-p"}},
}

func newCLI(name string, rc config.RuntimeConfig) (Runtime, error) {
	command := rc.Command
	args := rc.Args
	if preset, ok := cliPresets[rc.Type]; ok {
		if command == "" {
			command = preset.command
		}
		if args == nil {
			args = preset.args
		}
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
	if c.promptMode == "arg" {
		args = append(args, prompt)
	} else {
		stdin = bytes.NewReader([]byte(prompt))
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
