package schema

import (
	"encoding/json"
	"strings"
)

// Sample builds a best-effort example for the mock runtime and demos, not a
// general schema solver. It uses const, enum, examples, default, type,
// properties, items, size and numeric bounds, and local $refs. Alternatives
// choose the first branch; allOf combines object samples where possible.
// Callers still validate the result against the complete schema.
func (s *Schema) Sample() string {
	v := sampleValue(s.Doc, s.Doc, 0)
	encoded, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return "{}"
	}
	return string(encoded)
}

const maxSampleDepth = 12

func sampleValue(node, root any, depth int) any {
	obj, ok := node.(map[string]any)
	if !ok || depth > maxSampleDepth {
		return nil
	}
	if ref, ok := obj["$ref"].(string); ok {
		if target := resolveLocalRef(root, ref); target != nil {
			return sampleValue(target, root, depth+1)
		}
	}
	if c, ok := obj["const"]; ok {
		return c
	}
	if enum, ok := obj["enum"].([]any); ok && len(enum) > 0 {
		return enum[0]
	}
	if examples, ok := obj["examples"].([]any); ok && len(examples) > 0 {
		return examples[0]
	}
	if d, ok := obj["default"]; ok {
		return d
	}
	for _, kw := range []string{"oneOf", "anyOf"} {
		if branches, ok := obj[kw].([]any); ok && len(branches) > 0 {
			return sampleValue(branches[0], root, depth+1)
		}
	}
	if all, ok := obj["allOf"].([]any); ok && len(all) > 0 {
		merged := map[string]any{}
		for _, part := range all {
			if m, ok := sampleValue(part, root, depth+1).(map[string]any); ok {
				for k, v := range m {
					merged[k] = v
				}
			}
		}
		if len(merged) > 0 {
			return merged
		}
		return sampleValue(all[0], root, depth+1)
	}

	switch schemaType(obj) {
	case "object":
		out := map[string]any{}
		props, _ := obj["properties"].(map[string]any)
		for name, prop := range props {
			out[name] = sampleValue(prop, root, depth+1)
		}
		return out
	case "array":
		n := 1
		if min, ok := intKeyword(obj, "minItems"); ok && min > n {
			n = min
		}
		if max, ok := intKeyword(obj, "maxItems"); ok && max < n {
			n = max
		}
		items := make([]any, 0, n)
		for i := 0; i < n; i++ {
			items = append(items, sampleValue(obj["items"], root, depth+1))
		}
		return items
	case "string":
		return sampleString(obj)
	case "integer", "number":
		return sampleNumber(obj, schemaType(obj) == "integer")
	case "boolean":
		return true
	case "null":
		return nil
	}
	return nil
}

func schemaType(obj map[string]any) string {
	switch t := obj["type"].(type) {
	case string:
		return t
	case []any:
		for _, v := range t {
			if s, ok := v.(string); ok && s != "null" {
				return s
			}
		}
	}
	if _, ok := obj["properties"]; ok {
		return "object"
	}
	if _, ok := obj["items"]; ok {
		return "array"
	}
	return ""
}

func sampleString(obj map[string]any) string {
	var s string
	switch obj["format"] {
	case "date":
		s = "2024-01-31"
	case "date-time":
		s = "2024-01-31T12:00:00Z"
	case "email":
		s = "user@example.com"
	case "uri", "url":
		s = "https://example.com"
	case "uuid":
		s = "123e4567-e89b-12d3-a456-426614174000"
	default:
		s = "sample"
	}
	if min, ok := intKeyword(obj, "minLength"); ok && len(s) < min {
		s += strings.Repeat("x", min-len(s))
	}
	if max, ok := intKeyword(obj, "maxLength"); ok && len(s) > max {
		s = s[:max]
	}
	return s
}

func sampleNumber(obj map[string]any, integer bool) any {
	value := 1.0
	if min, ok := floatKeyword(obj, "minimum"); ok && value < min {
		value = min
	}
	if min, ok := floatKeyword(obj, "exclusiveMinimum"); ok && value <= min {
		value = min + 1
	}
	if max, ok := floatKeyword(obj, "maximum"); ok && value > max {
		value = max
	}
	if max, ok := floatKeyword(obj, "exclusiveMaximum"); ok && value >= max {
		value = max - 1
	}
	if integer {
		return int64(value)
	}
	return value
}

func intKeyword(obj map[string]any, key string) (int, bool) {
	f, ok := floatKeyword(obj, key)
	return int(f), ok
}

func floatKeyword(obj map[string]any, key string) (float64, bool) {
	switch v := obj[key].(type) {
	case json.Number:
		f, err := v.Float64()
		return f, err == nil
	case float64:
		return v, true
	}
	return 0, false
}

func resolveLocalRef(root any, ref string) any {
	if !strings.HasPrefix(ref, "#") {
		return nil
	}
	cur := root
	for _, part := range strings.Split(strings.TrimPrefix(strings.TrimPrefix(ref, "#"), "/"), "/") {
		if part == "" {
			continue
		}
		part = strings.ReplaceAll(strings.ReplaceAll(part, "~1", "/"), "~0", "~")
		m, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = m[part]
	}
	return cur
}
