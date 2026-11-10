package pipeline

import (
	"fmt"
	"strings"
)

// OpenSpecSpec describes a command, not an LLM invocation. Args are passed
// directly to the executable, without a shell or the incoming handoff.
type OpenSpecSpec struct {
	Command string   `yaml:"command,omitempty" json:"command,omitempty"`
	Args    []string `yaml:"args" json:"args"`
}

func validateOpenSpec(n *Node) error {
	if n.OpenSpec == nil || len(n.OpenSpec.Args) == 0 {
		return fmt.Errorf("openspec agent %q requires openspec.args", n.ID)
	}
	if n.Runtime != "" || n.Prompt != "" || len(n.Stages) > 0 {
		return fmt.Errorf("openspec agent %q may not define runtime, prompt, or stages", n.ID)
	}
	if n.TimeoutSeconds < 0 {
		return fmt.Errorf("openspec agent %q: timeout_seconds must not be negative", n.ID)
	}
	if strings.ContainsRune(n.OpenSpec.Command, '\x00') {
		return fmt.Errorf("openspec agent %q: command must not contain NUL", n.ID)
	}
	for _, arg := range n.OpenSpec.Args {
		if strings.TrimSpace(arg) == "" || strings.ContainsRune(arg, '\x00') {
			return fmt.Errorf("openspec agent %q: args must be non-empty strings without NUL", n.ID)
		}
	}
	return nil
}
