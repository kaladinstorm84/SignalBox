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
	"github.com/kaladinstorm84/SignalBox/internal/plugin"
)

// CodeOutcomeError translates a configured script exit into engine routing:
// fail is terminal, while retry requests rework of upstream execution steps.
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

// NewCode resolves a script relative to the project, delivers raw input on
// stdin, and optionally maps exits to outcomes. Scripts run as the host user,
// not in a sandbox; construction checks the file but does not execute it.
func NewCode(node *pipeline.Node, projectRoot string) (Runtime, error) {
	if node.Type.IsPlugin() {
		current, _, err := plugin.Load(projectRoot, node.Type.PluginName())
		if err != nil {
			return nil, err
		}
		if node.Plugin == nil || node.Plugin.Digest != current.Digest {
			return nil, fmt.Errorf("plugin %q changed since the pipeline was loaded; reload before running", current.Name)
		}
	}
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
		if node.Type.IsPlugin() {
			// Keep interpreter caches out of the versioned plugin folder.
			args = []string{"-B", "-X", "utf8", source}
		}
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
	rt.(*cliRuntime).code = true
	if node.Codes != nil {
		return &codeRuntime{Runtime: rt, codes: node.Codes}, nil
	}
	return rt, nil
}
