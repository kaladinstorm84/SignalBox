package pipeline

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/kaladinstorm84/SignalBox/internal/schema"
)

// Structured output settings.
const (
	OnInvalidRepair   = "repair"
	OnInvalidFail     = "fail"
	DefaultMaxRepairs = 2
	MaxRepairsLimit   = 10
	// DefaultSchemasDir is the schemas directory under the project root.
	DefaultSchemasDir = "schemas"
)

// OutputSpec requires a node's output to be JSON conforming to a schema
// from the project's schemas directory.
type OutputSpec struct {
	Schema string `yaml:"schema" json:"schema"`
	// OnInvalid is repair (re-invoke with the validation errors) or fail.
	// Agents default to repair; code nodes default to fail.
	OnInvalid string `yaml:"on_invalid,omitempty" json:"on_invalid,omitempty"`
	// MaxRepairs bounds repair attempts (default 2, at most 10).
	MaxRepairs *int `yaml:"max_repairs,omitempty" json:"max_repairs,omitempty"`
}

// InputSpec validates an execution's initial input before it starts.
type InputSpec struct {
	Schema string `yaml:"schema" json:"schema"`
}

// OutputSchema returns the node's output schema name, or "".
func (n *Node) OutputSchema() string {
	if n.Output == nil {
		return ""
	}
	return n.Output.Schema
}

// InvalidPolicy returns what happens when output fails validation.
func (n *Node) InvalidPolicy() string {
	if n.Output == nil {
		return ""
	}
	if n.Output.OnInvalid != "" {
		return n.Output.OnInvalid
	}
	if n.Type.BaseType() == TypeCode {
		return OnInvalidFail
	}
	return OnInvalidRepair
}

// RepairLimit returns how many repair attempts follow an invalid output.
func (n *Node) RepairLimit() int {
	if n.InvalidPolicy() != OnInvalidRepair {
		return 0
	}
	if n.Output.MaxRepairs != nil {
		return *n.Output.MaxRepairs
	}
	return DefaultMaxRepairs
}

// SchemaFor returns a schema the pipeline references, resolved at load time.
func (p *Pipeline) SchemaFor(name string) *schema.Schema {
	return p.schemas[name]
}

// InputSchema returns the pipeline's input schema name, or "".
func (p *Pipeline) InputSchema() string {
	if p.Input == nil {
		return ""
	}
	return p.Input.Schema
}

// SchemaNames lists the schemas the pipeline references, sorted.
func (p *Pipeline) SchemaNames() []string {
	seen := map[string]bool{}
	if name := p.InputSchema(); name != "" {
		seen[name] = true
	}
	if p.Files != nil && p.Files.Schema != "" {
		seen[p.Files.Schema] = true
	}
	for i := range p.Nodes {
		if name := p.Nodes[i].OutputSchema(); name != "" {
			seen[name] = true
		}
	}
	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func validSchemaName(name string) bool {
	return name != "" && !strings.ContainsAny(name, `/\`) && name != "." && name != ".." && filepath.Base(name) == name
}

func (p *Pipeline) validateInputSpec() error {
	if p.Input == nil {
		return nil
	}
	if !validSchemaName(p.Input.Schema) {
		return fmt.Errorf("input.schema must name a schema file in the schemas directory (without extension)")
	}
	return nil
}

func validateOutputSpec(n *Node) error {
	if n.Output == nil {
		return nil
	}
	switch n.Type.BaseType() {
	case TypeStandard, TypeCode, TypeLLMGate:
	default:
		return fmt.Errorf("agent %q: output.schema is only supported on standard, code, and llm_gate nodes", n.ID)
	}
	if !validSchemaName(n.Output.Schema) {
		return fmt.Errorf("agent %q: output.schema must name a schema file in the schemas directory (without extension)", n.ID)
	}
	switch n.Output.OnInvalid {
	case "", OnInvalidRepair, OnInvalidFail:
	default:
		return fmt.Errorf("agent %q: output.on_invalid must be repair or fail", n.ID)
	}
	if n.Output.MaxRepairs != nil {
		if *n.Output.MaxRepairs < 0 || *n.Output.MaxRepairs > MaxRepairsLimit {
			return fmt.Errorf("agent %q: output.max_repairs must be between 0 and %d", n.ID, MaxRepairsLimit)
		}
		if n.InvalidPolicy() != OnInvalidRepair {
			return fmt.Errorf("agent %q: output.max_repairs requires on_invalid: repair", n.ID)
		}
	}
	return nil
}

// resolveSchemas attaches every referenced schema, loading the project's
// schemas directory when the options do not supply a set.
func (p *Pipeline) resolveSchemas(opts Options) error {
	names := p.SchemaNames()
	if len(names) == 0 {
		return nil
	}
	set := opts.Schemas
	if set == nil {
		dir := opts.SchemasDir
		if dir == "" && opts.Root != "" {
			dir = filepath.Join(opts.Root, DefaultSchemasDir)
		}
		if dir == "" {
			return fmt.Errorf("pipeline %q references schemas but no schemas directory is configured", p.Name)
		}
		set, _ = schema.LoadDir(dir)
	}
	p.schemas = map[string]*schema.Schema{}
	for _, name := range names {
		s, err := set.Get(name)
		if err != nil {
			return fmt.Errorf("pipeline %q: %w", p.Name, err)
		}
		p.schemas[name] = s
	}
	return nil
}
