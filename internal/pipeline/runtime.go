package pipeline

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// normalizeRuntime keeps the resolved representation flat while accepting a
// grouped runtime in YAML. This also preserves legacy scalar runtime/model files.
func normalizeRuntime(node *yaml.Node) (*yaml.Node, error) {
	if node.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("agent or retry attempt must be a mapping")
	}
	runtime := mapValue(node, "runtime")
	if runtime == nil {
		return node, nil
	}
	if runtime.Kind == yaml.AliasNode {
		runtime = runtime.Alias
	}
	if runtime.Kind == yaml.ScalarNode && runtime.Tag == "!!str" {
		return node, nil
	}
	if runtime.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("runtime must be a configured name or a mapping of provider, model, effort and speed")
	}
	for _, field := range []string{"model", "reasoning_effort", "speed"} {
		if mapValue(node, field) != nil {
			return nil, fmt.Errorf("grouped runtime cannot be combined with top-level %s; put settings inside runtime", field)
		}
	}
	out := cloneNode(node)
	deleteKey(out, "runtime")
	seen := map[string]bool{}
	for i := 0; i+1 < len(runtime.Content); i += 2 {
		key, value := runtime.Content[i].Value, runtime.Content[i+1]
		if seen[key] {
			return nil, fmt.Errorf("runtime repeats field %q", key)
		}
		seen[key] = true
		if value.Kind != yaml.ScalarNode || value.Tag != "!!str" || strings.TrimSpace(value.Value) == "" {
			return nil, fmt.Errorf("runtime.%s must be a non-empty string", key)
		}
		switch key {
		case "provider":
			setKey(out, "runtime", cloneNode(value))
		case "model", "speed":
			setKey(out, key, cloneNode(value))
		case "effort":
			setKey(out, "reasoning_effort", cloneNode(value))
		default:
			return nil, fmt.Errorf("unknown runtime setting %q (use provider, model, effort or speed)", key)
		}
	}
	return out, nil
}

func (n *Node) UnmarshalYAML(node *yaml.Node) error {
	normalized, err := normalizeRuntime(node)
	if err != nil {
		return err
	}
	type plain Node
	var decoded plain
	if err := normalized.Decode(&decoded); err != nil {
		return err
	}
	*n = Node(decoded)
	return nil
}

func (a *RetryAttempt) UnmarshalYAML(node *yaml.Node) error {
	normalized, err := normalizeRuntime(node)
	if err != nil {
		return err
	}
	for i := 0; i+1 < len(normalized.Content); i += 2 {
		switch normalized.Content[i].Value {
		case "runtime", "model", "reasoning_effort", "speed":
		default:
			return fmt.Errorf("unknown retry attempt setting %q", normalized.Content[i].Value)
		}
	}
	type plain RetryAttempt
	var decoded plain
	if err := normalized.Decode(&decoded); err != nil {
		return err
	}
	*a = RetryAttempt(decoded)
	return nil
}
