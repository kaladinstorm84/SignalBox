package runtime

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strings"

	"github.com/kaladinstorm84/SignalBox/internal/config"
	"github.com/kaladinstorm84/SignalBox/internal/pipeline"
)

// NewOpenSpec runs a configured CLI operation in the SignalBox project root.
// Arguments are passed individually; incoming handoff text is neither appended
// to them nor sent on stdin.
func NewOpenSpec(node *pipeline.Node, projectRoot string) (Runtime, error) {
	if node.OpenSpec == nil || len(node.OpenSpec.Args) == 0 {
		return nil, fmt.Errorf("openspec agent %q requires openspec.args", node.ID)
	}
	if projectRoot == "" {
		return nil, fmt.Errorf("openspec agent %q requires a project root", node.ID)
	}
	root, err := filepath.Abs(projectRoot)
	if err != nil {
		return nil, fmt.Errorf("openspec agent %q: %w", node.ID, err)
	}
	info, err := os.Stat(root)
	if err != nil {
		return nil, fmt.Errorf("openspec agent %q: %w", node.ID, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("openspec agent %q: project root must be a directory", node.ID)
	}
	command := node.OpenSpec.Command
	if command == "" {
		command = "openspec"
	}
	rt, err := newCLI(node.ID, config.RuntimeConfig{
		Type: "cli", Command: command, Args: append([]string(nil), node.OpenSpec.Args...),
		TimeoutSeconds: node.TimeoutSeconds,
	})
	if err != nil {
		return nil, err
	}
	cli := rt.(*cliRuntime)
	cli.dir = root
	cli.kind = "openspec"
	cli.commandOnly = true
	return cli, nil
}

// npm's Windows shims require a shell. Run the package entry point through
// Node instead, keeping parameter values out of shell command strings.
func openSpecCommand(command string, args []string) (string, []string, error) {
	if goruntime.GOOS != "windows" {
		return command, args, nil
	}
	path, err := exec.LookPath(command)
	if err != nil {
		return "", nil, fmt.Errorf("finding OpenSpec executable %q: %w", command, err)
	}
	ext := strings.ToLower(filepath.Ext(path))
	if ext != ".cmd" && ext != ".bat" {
		return path, args, nil
	}
	dir := filepath.Dir(path)
	candidates := []string{
		filepath.Join(dir, "node_modules", "@fission-ai", "openspec", "bin", "openspec.js"),
		filepath.Join(dir, "..", "@fission-ai", "openspec", "bin", "openspec.js"),
	}
	for _, entry := range candidates {
		info, err := os.Stat(entry)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return "", nil, fmt.Errorf("reading OpenSpec entry point: %w", err)
		}
		if !info.Mode().IsRegular() {
			return "", nil, fmt.Errorf("OpenSpec entry point %q must be a regular file", entry)
		}
		node := filepath.Join(dir, "node.exe")
		if _, err := os.Stat(node); os.IsNotExist(err) {
			node, err = exec.LookPath("node")
			if err != nil {
				return "", nil, fmt.Errorf("OpenSpec requires Node.js: %w", err)
			}
		} else if err != nil {
			return "", nil, fmt.Errorf("reading Node.js executable: %w", err)
		}
		return node, append([]string{entry}, args...), nil
	}
	return "", nil, fmt.Errorf("OpenSpec command %q is a Windows shell shim without a discoverable npm entry point; set openspec.command to node and put the absolute path to bin/openspec.js first in openspec.args", path)
}
