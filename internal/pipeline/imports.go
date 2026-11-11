package pipeline

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/kaladinstorm84/SignalBox/internal/schema"
	"gopkg.in/yaml.v3"
)

// Import limits.
const (
	MaxImportDepth  = 10
	MaxExtendsDepth = 10
)

// Options controls how pipeline files are loaded.
type Options struct {
	// Root bounds import paths. Imports resolving outside it are rejected
	// unless AllowExternal is set. Empty means the pipeline file's directory.
	Root string
	// AllowExternal permits imports outside Root (signalbox.yaml
	// allow_external_imports).
	AllowExternal bool
	// SchemasDir holds the project's JSON Schema files (default
	// Root/schemas).
	SchemasDir string
	// Schemas, when set, is used instead of loading SchemasDir.
	Schemas *schema.Set
}

// ParseFile validates pipeline YAML read from path, resolving imports
// relative to that path.
func ParseFile(data []byte, path string, opts Options) (*Pipeline, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if opts.Root == "" {
		opts.Root = filepath.Dir(abs)
	}
	if opts.Root, err = filepath.Abs(opts.Root); err != nil {
		return nil, err
	}
	return parse(data, abs, opts)
}

func parse(data []byte, path string, opts Options) (*Pipeline, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("invalid YAML: %w", err)
	}
	var p Pipeline
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		if err := yaml.Unmarshal(data, &p); err != nil {
			return nil, fmt.Errorf("invalid YAML: %w", err)
		}
	} else {
		root := doc.Content[0]
		r := &resolver{opts: opts, seen: map[string]bool{}}
		lib, err := r.resolve(root, path, nil)
		if err != nil {
			return nil, err
		}
		if lib.changed {
			agents := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
			for _, a := range lib.agents {
				agents.Content = append(agents.Content, a.node)
			}
			deleteKey(root, "imports")
			deleteKey(root, "templates")
			setKey(root, "agents", agents)
		}
		if err := root.Decode(&p); err != nil {
			return nil, fmt.Errorf("invalid YAML: %w", err)
		}
		p.Sources = r.sources
	}
	if err := p.validate(); err != nil {
		return nil, err
	}
	if err := p.resolveSchemas(opts); err != nil {
		return nil, err
	}
	return &p, nil
}

// ParseWith validates pipeline YAML from memory using opts for schema
// resolution. Imports are not available without a file location.
func ParseWith(data []byte, opts Options) (*Pipeline, error) {
	return parse(data, "", opts)
}

type entry struct {
	id     string
	node   *yaml.Node
	source string
}

type library struct {
	agents    []entry
	templates map[string]entry
	// changed reports that imports, templates, or extends were applied.
	changed bool
}

type resolver struct {
	opts    Options
	sources []string
	seen    map[string]bool
}

type importSpec struct {
	From      string    `yaml:"from"`
	Agents    *[]string `yaml:"agents"`
	Templates *[]string `yaml:"templates"`
}

// resolve expands the imports, templates, and extends of one file. The
// returned agents and templates are fully merged.
func (r *resolver) resolve(root *yaml.Node, path string, stack []string) (*library, error) {
	where := r.display(path)
	lib := &library{templates: map[string]entry{}}

	var imported []entry
	importedTemplates := map[string]entry{}
	if node := mapValue(root, "imports"); node != nil {
		lib.changed = true
		if path == "" {
			return nil, fmt.Errorf("imports require the pipeline to be loaded from a file")
		}
		var specs []importSpec
		if err := node.Decode(&specs); err != nil {
			return nil, fmt.Errorf("%s: imports must be a list of {from, agents, templates}: %w", where, err)
		}
		if len(stack) >= MaxImportDepth {
			return nil, fmt.Errorf("%s: imports nested deeper than %d files", where, MaxImportDepth)
		}
		for _, spec := range specs {
			target, err := r.importPath(spec.From, filepath.Dir(path))
			if err != nil {
				return nil, fmt.Errorf("%s: import %q: %w", where, spec.From, err)
			}
			chain := append(append([]string{}, stack...), path)
			for _, previous := range chain {
				if previous == target {
					names := make([]string, 0, len(chain)+1)
					for _, c := range append(chain, target) {
						names = append(names, r.display(c))
					}
					return nil, fmt.Errorf("import cycle: %s", strings.Join(names, " -> "))
				}
			}
			data, err := os.ReadFile(target)
			if err != nil {
				return nil, fmt.Errorf("%s: import %q: %w", where, spec.From, err)
			}
			var doc yaml.Node
			if err := yaml.Unmarshal(data, &doc); err != nil {
				return nil, fmt.Errorf("%s: invalid YAML: %w", r.display(target), err)
			}
			if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
				return nil, fmt.Errorf("%s: an imported file must be a mapping with agents and/or templates", r.display(target))
			}
			sub, err := r.resolve(doc.Content[0], target, chain)
			if err != nil {
				return nil, err
			}
			if !r.seen[target] {
				r.seen[target] = true
				r.sources = append(r.sources, r.display(target))
			}

			agents := sub.agents
			if spec.Agents != nil {
				agents = nil
				for _, id := range *spec.Agents {
					found := false
					for _, a := range sub.agents {
						if a.id == id {
							agents = append(agents, a)
							found = true
							break
						}
					}
					if !found {
						return nil, fmt.Errorf("%s: import %q has no agent %q", where, spec.From, id)
					}
				}
			}
			for _, a := range agents {
				if err := r.addUnique(&imported, a); err != nil {
					return nil, fmt.Errorf("%s: %w", where, err)
				}
			}
			names := sortedKeys(sub.templates)
			if spec.Templates != nil {
				names = *spec.Templates
			}
			for _, id := range names {
				t, ok := sub.templates[id]
				if !ok {
					return nil, fmt.Errorf("%s: import %q has no template %q", where, spec.From, id)
				}
				if existing, dup := importedTemplates[id]; dup && existing.source != t.source {
					return nil, fmt.Errorf("%s: template %q is defined in both %s and %s", where, id, r.display(existing.source), r.display(t.source))
				}
				importedTemplates[id] = t
			}
		}
	}

	// Local templates, resolved against local and imported templates.
	raw := map[string]entry{}
	var localOrder []string
	if node := mapValue(root, "templates"); node != nil {
		lib.changed = true
		if node.Kind != yaml.SequenceNode {
			return nil, fmt.Errorf("%s: templates must be a list", where)
		}
		for i, t := range node.Content {
			id := scalarValue(t, "id")
			if t.Kind != yaml.MappingNode || id == "" {
				return nil, fmt.Errorf("%s: template #%d must be a mapping with an id", where, i+1)
			}
			if _, dup := raw[id]; dup {
				return nil, fmt.Errorf("%s: duplicate template id %q", where, id)
			}
			if existing, dup := importedTemplates[id]; dup {
				return nil, fmt.Errorf("%s: template %q is defined in both %s and %s", where, id, where, r.display(existing.source))
			}
			raw[id] = entry{id: id, node: t, source: path}
			localOrder = append(localOrder, id)
		}
	}
	table := &templateTable{raw: raw, resolved: map[string]*yaml.Node{}, imported: importedTemplates, where: where}
	for id, t := range importedTemplates {
		lib.templates[id] = t
	}
	for _, id := range localOrder {
		node, err := table.get(id, nil)
		if err != nil {
			return nil, err
		}
		lib.templates[id] = entry{id: id, node: node, source: path}
	}

	// Local agents first (applying extends), then imported agents.
	if node := mapValue(root, "agents"); node != nil && node.Kind != yaml.SequenceNode {
		return nil, fmt.Errorf("%s: agents must be a list", where)
	} else if node != nil {
		for _, a := range node.Content {
			merged := a
			if a.Kind == yaml.MappingNode && mapValue(a, "extends") != nil {
				lib.changed = true
				bases, err := extendsList(a)
				if err != nil {
					return nil, fmt.Errorf("%s: agent %q: %w", where, scalarValue(a, "id"), err)
				}
				var base *yaml.Node
				for _, b := range bases {
					resolved, err := table.get(b, nil)
					if err != nil {
						return nil, fmt.Errorf("%s: agent %q: %w", where, scalarValue(a, "id"), err)
					}
					base = mergeNodes(base, withoutKeys(resolved, "id"))
				}
				merged = mergeNodes(base, withoutKeys(a, "extends"))
			}
			lib.agents = append(lib.agents, entry{id: scalarValue(merged, "id"), node: merged, source: path})
		}
	}
	if len(imported) > 0 {
		lib.changed = true
		local := map[string]bool{}
		for _, a := range lib.agents {
			local[a.id] = true
		}
		for _, a := range imported {
			if local[a.id] {
				return nil, fmt.Errorf("%s: agent %q is defined locally and imported from %s", where, a.id, r.display(a.source))
			}
			lib.agents = append(lib.agents, a)
		}
	}
	return lib, nil
}

func (r *resolver) addUnique(list *[]entry, e entry) error {
	for _, existing := range *list {
		if existing.id != e.id {
			continue
		}
		if existing.source == e.source {
			return nil
		}
		return fmt.Errorf("agent %q is imported from both %s and %s", e.id, r.display(existing.source), r.display(e.source))
	}
	*list = append(*list, e)
	return nil
}

type templateTable struct {
	raw      map[string]entry
	resolved map[string]*yaml.Node
	imported map[string]entry
	where    string
}

// get returns a template with its extends chain merged in.
func (t *templateTable) get(id string, chain []string) (*yaml.Node, error) {
	if node, ok := t.resolved[id]; ok {
		return node, nil
	}
	if imported, ok := t.imported[id]; ok {
		return imported.node, nil
	}
	e, ok := t.raw[id]
	if !ok {
		return nil, fmt.Errorf("extends unknown template %q", id)
	}
	for _, c := range chain {
		if c == id {
			return nil, fmt.Errorf("%s: template extends cycle: %s -> %s", t.where, strings.Join(chain, " -> "), id)
		}
	}
	if len(chain) >= MaxExtendsDepth {
		return nil, fmt.Errorf("%s: template %q extends deeper than %d levels", t.where, id, MaxExtendsDepth)
	}
	bases, err := extendsList(e.node)
	if err != nil {
		return nil, fmt.Errorf("%s: template %q: %w", t.where, id, err)
	}
	var base *yaml.Node
	for _, b := range bases {
		resolved, err := t.get(b, append(chain, id))
		if err != nil {
			return nil, err
		}
		base = mergeNodes(base, withoutKeys(resolved, "id"))
	}
	node := mergeNodes(base, withoutKeys(e.node, "extends"))
	t.resolved[id] = node
	return node, nil
}

// importPath resolves relative to the importing file, but enforces the project
// boundary unless external imports are explicitly enabled.
func (r *resolver) importPath(from, baseDir string) (string, error) {
	if strings.TrimSpace(from) == "" {
		return "", fmt.Errorf("from is required")
	}
	ext := strings.ToLower(filepath.Ext(from))
	if ext != ".yaml" && ext != ".yml" {
		return "", fmt.Errorf("imported files must be .yaml or .yml")
	}
	target := from
	if !filepath.IsAbs(target) {
		target = filepath.Join(baseDir, from)
	}
	target, err := filepath.Abs(target)
	if err != nil {
		return "", err
	}
	if r.opts.AllowExternal {
		return target, nil
	}
	root := r.opts.Root
	outside := !within(root, target)
	if !outside {
		// Follow symlinks so a link inside the project cannot escape it.
		realRoot, rootErr := filepath.EvalSymlinks(root)
		realTarget, targetErr := filepath.EvalSymlinks(target)
		if rootErr == nil && targetErr == nil && !within(realRoot, realTarget) {
			outside = true
		}
	}
	if outside {
		return "", fmt.Errorf("resolves outside the project root %s; set allow_external_imports: true in signalbox.yaml to permit it", root)
	}
	return target, nil
}

func within(root, target string) bool {
	rel, err := filepath.Rel(root, target)
	if err != nil || filepath.IsAbs(rel) {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func (r *resolver) display(path string) string {
	if path == "" {
		return "pipeline"
	}
	if r.opts.Root != "" {
		if rel, err := filepath.Rel(r.opts.Root, path); err == nil && within(r.opts.Root, path) {
			return filepath.ToSlash(rel)
		}
	}
	return path
}

func extendsList(node *yaml.Node) ([]string, error) {
	value := mapValue(node, "extends")
	if value == nil {
		return nil, nil
	}
	switch value.Kind {
	case yaml.ScalarNode:
		if value.Value == "" {
			return nil, fmt.Errorf("extends must name a template")
		}
		return []string{value.Value}, nil
	case yaml.SequenceNode:
		var names []string
		for _, item := range value.Content {
			if item.Kind != yaml.ScalarNode || item.Value == "" {
				return nil, fmt.Errorf("extends must be a template id or a list of template ids")
			}
			names = append(names, item.Value)
		}
		return names, nil
	default:
		return nil, fmt.Errorf("extends must be a template id or a list of template ids")
	}
}

// mergeNodes deep-merges child over base: mappings merge key by key, and
// any other value (including lists) in the child replaces the base value.
// The result is cloned so extending one template cannot mutate another agent.
func mergeNodes(base, child *yaml.Node) *yaml.Node {
	if base == nil {
		return cloneNode(child)
	}
	if base.Kind != yaml.MappingNode || child.Kind != yaml.MappingNode {
		return cloneNode(child)
	}
	out := cloneNode(base)
	for i := 0; i+1 < len(child.Content); i += 2 {
		key, value := child.Content[i].Value, child.Content[i+1]
		if existing := mapValue(out, key); existing != nil && existing.Kind == yaml.MappingNode && value.Kind == yaml.MappingNode {
			setKey(out, key, mergeNodes(existing, value))
			continue
		}
		setKey(out, key, cloneNode(value))
	}
	return out
}

func cloneNode(n *yaml.Node) *yaml.Node {
	if n == nil {
		return nil
	}
	c := *n
	c.Content = make([]*yaml.Node, len(n.Content))
	for i, child := range n.Content {
		c.Content[i] = cloneNode(child)
	}
	if n.Alias != nil {
		c.Alias = cloneNode(n.Alias)
	}
	return &c
}

func withoutKeys(n *yaml.Node, keys ...string) *yaml.Node {
	c := cloneNode(n)
	for _, key := range keys {
		deleteKey(c, key)
	}
	return c
}

func mapValue(n *yaml.Node, key string) *yaml.Node {
	if n == nil || n.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return n.Content[i+1]
		}
	}
	return nil
}

func scalarValue(n *yaml.Node, key string) string {
	if v := mapValue(n, key); v != nil && v.Kind == yaml.ScalarNode {
		return v.Value
	}
	return ""
}

func setKey(n *yaml.Node, key string, value *yaml.Node) {
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			n.Content[i+1] = value
			return
		}
	}
	n.Content = append(n.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, value)
}

func deleteKey(n *yaml.Node, key string) {
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			n.Content = append(n.Content[:i], n.Content[i+2:]...)
			return
		}
	}
}

func sortedKeys(m map[string]entry) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
