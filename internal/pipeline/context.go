package pipeline

import "fmt"

// Context strategies select which earlier execution outputs a node receives
// in addition to its normal input.
const (
	ContextNone   = "none"
	ContextAll    = "all"
	ContextRecent = "recent"
	ContextOwn    = "own"

	DefaultRecentWindow = 1
	MaxContextWindow    = 100
	MaxContextChars     = 1_000_000
)

// ContextConfig selects prior work to append to a node's runtime input.
// Omitting it (or strategy none) keeps the node's input unchanged.
type ContextConfig struct {
	Strategy string `yaml:"strategy" json:"strategy"`
	// Window is the number of entries kept by recent (default 1) and own
	// (default 0, meaning every earlier output of the node).
	Window int `yaml:"window,omitempty" json:"window,omitempty"`
	// MaxChars caps the characters of included outputs; 0 means no cap.
	MaxChars int `yaml:"max_chars,omitempty" json:"max_chars,omitempty"`
}

// ContextStrategy returns the node's strategy, defaulting to none.
func (n *Node) ContextStrategy() string {
	if n.Context == nil || n.Context.Strategy == "" {
		return ContextNone
	}
	return n.Context.Strategy
}

// validateContext checks a node's context block and fills in defaults.
func validateContext(n *Node) error {
	c := n.Context
	if c == nil {
		return nil
	}
	switch n.Type.BaseType() {
	case TypeStandard, TypeCode, TypeLLMGate, TypeSession:
	default:
		return fmt.Errorf("agent %q: context is only supported on standard, code, llm_gate, and session nodes", n.ID)
	}
	if c.Window < 0 || c.Window > MaxContextWindow {
		return fmt.Errorf("agent %q: context.window must be between 0 and %d", n.ID, MaxContextWindow)
	}
	if c.MaxChars < 0 || c.MaxChars > MaxContextChars {
		return fmt.Errorf("agent %q: context.max_chars must be between 0 and %d", n.ID, MaxContextChars)
	}
	switch c.Strategy {
	case "":
		return fmt.Errorf("agent %q: context.strategy is required (none, all, recent, or own)", n.ID)
	case ContextNone:
		if c.Window != 0 || c.MaxChars != 0 {
			return fmt.Errorf("agent %q: context strategy none does not accept window or max_chars", n.ID)
		}
	case ContextAll:
		if c.Window != 0 {
			return fmt.Errorf("agent %q: context strategy all does not accept window; use recent", n.ID)
		}
	case ContextRecent:
		if c.Window == 0 {
			c.Window = DefaultRecentWindow
		}
	case ContextOwn:
	default:
		return fmt.Errorf("agent %q: unknown context strategy %q (want none, all, recent, or own)", n.ID, c.Strategy)
	}
	return nil
}
