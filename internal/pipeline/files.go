package pipeline

import (
	"fmt"
	"regexp"
	"strings"
)

// File delivery modes for a node's files: setting.
const (
	FilesNone    = "none"
	FilesInline  = "inline"
	FilesPath    = "path"
	FilesInherit = "inherit"

	// DefaultFilesBudget is the character budget for inline file delivery.
	DefaultFilesBudget = 20000
	// MaxFilesBudget bounds files_budget.
	MaxFilesBudget = 1000000
)

// FilesSpec declares the files an execution of the pipeline accepts.
type FilesSpec struct {
	// Required rejects an execution started without any files.
	Required bool `yaml:"required,omitempty" json:"required,omitempty"`
	// MaxCount limits the number of files (0 means the project limit).
	MaxCount int `yaml:"max_count,omitempty" json:"max_count,omitempty"`
	// Extensions lists accepted extensions such as .csv (empty means the
	// project's allowed extensions).
	Extensions []string `yaml:"extensions,omitempty" json:"extensions,omitempty"`
	// Schema validates uploaded .json, .yaml, and .yml files.
	Schema string `yaml:"schema,omitempty" json:"schema,omitempty"`
}

var extensionPattern = regexp.MustCompile(`^\.[a-z0-9][a-z0-9_+-]{0,15}$`)

// NormalizeExtension lowercases an extension and adds the leading dot.
func NormalizeExtension(ext string) string {
	ext = strings.ToLower(strings.TrimSpace(ext))
	if ext != "" && !strings.HasPrefix(ext, ".") {
		ext = "." + ext
	}
	return ext
}

// ValidExtension reports whether ext (normalized) is a plausible extension.
func ValidExtension(ext string) bool {
	return extensionPattern.MatchString(ext)
}

// FilesMode returns the node's file delivery mode, defaulting to none.
func (n *Node) FilesMode() string {
	if n.Files == "" {
		return FilesNone
	}
	return n.Files
}

// InlineBudget returns the character budget for inline delivery.
func (n *Node) InlineBudget() int {
	if n.FilesBudget > 0 {
		return n.FilesBudget
	}
	return DefaultFilesBudget
}

// FileParams lists the parameters declared with type: file, sorted.
func (p *Pipeline) FileParams() []string {
	var names []string
	for _, name := range p.ParamNames() {
		if p.Params[name] != nil && p.Params[name].Type == ParamFile {
			names = append(names, name)
		}
	}
	return names
}

func (p *Pipeline) validateFilesSpec() error {
	f := p.Files
	if f == nil {
		return nil
	}
	if f.MaxCount < 0 {
		return fmt.Errorf("files.max_count must not be negative")
	}
	seen := map[string]bool{}
	for i, ext := range f.Extensions {
		norm := NormalizeExtension(ext)
		if !ValidExtension(norm) {
			return fmt.Errorf("files.extensions: %q is not a file extension such as .csv", ext)
		}
		if seen[norm] {
			return fmt.Errorf("files.extensions lists %q more than once", norm)
		}
		seen[norm] = true
		f.Extensions[i] = norm
	}
	if f.Schema != "" && !validSchemaName(f.Schema) {
		return fmt.Errorf("files.schema must name a schema file in the schemas directory (without extension)")
	}
	return nil
}

func validateNodeFiles(n *Node) error {
	if n.Files == "" && n.FilesBudget == 0 {
		return nil
	}
	switch n.Type {
	case TypeStandard, TypeCode, TypeLLMGate:
		switch n.Files {
		case "", FilesNone, FilesInline, FilesPath:
		default:
			return fmt.Errorf("agent %q: files must be inline, path, or none", n.ID)
		}
	case TypePipeline:
		switch n.Files {
		case "", FilesNone, FilesInherit:
		default:
			return fmt.Errorf("agent %q: files on a pipeline node must be inherit or none", n.ID)
		}
	default:
		return fmt.Errorf("agent %q: files is only supported on standard, code, llm_gate, and pipeline nodes", n.ID)
	}
	if n.FilesBudget != 0 {
		if n.Files != FilesInline {
			return fmt.Errorf("agent %q: files_budget requires files: inline", n.ID)
		}
		if n.FilesBudget < 0 || n.FilesBudget > MaxFilesBudget {
			return fmt.Errorf("agent %q: files_budget must be between 1 and %d", n.ID, MaxFilesBudget)
		}
	}
	return nil
}
