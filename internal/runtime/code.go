package runtime

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/kaladinstorm84/SignalBox/internal/config"
	"github.com/kaladinstorm84/SignalBox/internal/pipeline"
)

type CodeOutcomeError struct {
	Outcome  string
	ExitCode int
	Feedback string
}

func (e *CodeOutcomeError) Error() string {
	return fmt.Sprintf("code exit %d (%s): %s", e.ExitCode, e.Outcome, e.Feedback)
}

type codeRuntime struct {
	Runtime
	codes map[string]int
}

func (c *codeRuntime) Invoke(ctx context.Context, req Request) (string, error) {
	output, err := c.Runtime.Invoke(ctx, req)
	exitCode := 0
	stderr := ""
	if err != nil {
		var exited *ProcessExitError
		if !errors.As(err, &exited) {
			return "", err
		}
		exitCode, output, stderr = exited.ExitCode, exited.Stdout, exited.Stderr
	}
	if exitCode == c.codes["success"] {
		return output, nil
	}
	for _, outcome := range []string{"fail", "retry"} {
		if exitCode == c.codes[outcome] {
			feedback := strings.TrimSpace(output + "\n\n" + stderr)
			if feedback == "" {
				feedback = fmt.Sprintf("script returned exit code %d", exitCode)
			}
			return output, &CodeOutcomeError{Outcome: outcome, ExitCode: exitCode, Feedback: feedback}
		}
	}
	if err == nil {
		err = fmt.Errorf("unmapped script exit code %d", exitCode)
	}
	return "", err
}

func NewCode(node *pipeline.Node, projectRoot string) (Runtime, error) {
	root, err := filepath.Abs(projectRoot)
	if err != nil {
		return nil, err
	}
	source := node.CodeSource
	if !filepath.IsAbs(source) {
		source = filepath.Join(root, source)
	}
	info, err := os.Stat(source)
	if err != nil {
		return nil, fmt.Errorf("code agent %q: %w", node.ID, err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("code agent %q: code-source must be a regular file", node.ID)
	}
	command := ""
	args := []string{source}
	switch node.Language {
	case "python":
		command = "python"
		args = []string{"-X", "utf8", source}
	case "javascript":
		command = "node"
	case "powershell":
		command = "powershell"
		args = []string{"-NoProfile", "-NonInteractive", "-File", source}
	default:
		return nil, fmt.Errorf("code agent %q: unsupported language %q", node.ID, node.Language)
	}
	rt, err := newCLI(node.ID, config.RuntimeConfig{
		Type: "cli", Command: command, Args: args, PromptMode: "stdin", TimeoutSeconds: node.TimeoutSeconds,
	})
	if err != nil {
		return nil, err
	}
	rt.(*cliRuntime).dir = root
	if node.Codes != nil {
		return &codeRuntime{Runtime: rt, codes: node.Codes}, nil
	}
	return rt, nil
}
