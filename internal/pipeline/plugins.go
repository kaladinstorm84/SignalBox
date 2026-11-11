package pipeline

import (
	"fmt"
	"path"
	"strings"

	"github.com/kaladinstorm84/SignalBox/internal/plugin"
)

// PluginName returns the folder name of a plugin node, or empty for built-ins.
func (t NodeType) PluginName() string {
	if !t.IsPlugin() {
		return ""
	}
	return strings.TrimPrefix(string(t), "plugin:")
}

func (t NodeType) IsPlugin() bool {
	return strings.HasPrefix(string(t), "plugin:")
}

// BaseType preserves the public plugin type while sharing code-node semantics.
func (t NodeType) BaseType() NodeType {
	if t.IsPlugin() {
		return TypeCode
	}
	return t
}

func (p *Pipeline) resolvePlugins(root string) error {
	cache := map[string]*plugin.Definition{}
	for i := range p.Nodes {
		n := &p.Nodes[i]
		if !n.Type.IsPlugin() {
			continue
		}
		name := n.Type.PluginName()
		if !plugin.ValidName(name) {
			return fmt.Errorf("agent %q: invalid plugin type %q", n.ID, n.Type)
		}
		if n.Language != "" || n.CodeSource != "" || n.Codes != nil || n.TimeoutSeconds != 0 {
			return fmt.Errorf("agent %q: plugin language, entry, timeout and exit codes belong in plugins/%s/plugin.yaml", n.ID, name)
		}
		def := cache[name]
		if def == nil {
			var err error
			def, _, err = plugin.Load(root, name)
			if err != nil {
				return fmt.Errorf("agent %q: %w", n.ID, err)
			}
			cache[name] = def
		}
		n.Plugin = def
		n.Language, n.CodeSource = def.Language, path.Join("plugins", name, def.Entry)
		n.Codes, n.TimeoutSeconds = def.Codes, def.TimeoutSeconds
	}
	return nil
}
