package pipeline

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Parameter types.
const (
	ParamString  = "string"
	ParamInteger = "integer"
	ParamNumber  = "number"
	ParamBoolean = "boolean"

	MaxParams           = 50
	MaxParamValueLength = 10000
)

// Param declares one typed pipeline parameter. A YAML scalar is shorthand for
// an optional string parameter with that default.
type Param struct {
	Type        string   `yaml:"type,omitempty" json:"type"`
	Default     *string  `yaml:"-" json:"default,omitempty"`
	Required    bool     `yaml:"required,omitempty" json:"required,omitempty"`
	Description string   `yaml:"description,omitempty" json:"description,omitempty"`
	Values      []string `yaml:"values,omitempty" json:"values,omitempty"`
}

func (p *Param) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind == yaml.ScalarNode {
		value := node.Value
		*p = Param{Type: ParamString, Default: &value}
		return nil
	}
	var raw struct {
		Type        string    `yaml:"type"`
		Default     yaml.Node `yaml:"default"`
		Required    bool      `yaml:"required"`
		Description string    `yaml:"description"`
		Values      []string  `yaml:"values"`
	}
	if err := node.Decode(&raw); err != nil {
		return err
	}
	*p = Param{Type: raw.Type, Required: raw.Required, Description: raw.Description, Values: raw.Values}
	if raw.Default.Kind != 0 {
		if raw.Default.Kind != yaml.ScalarNode {
			return fmt.Errorf("line %d: parameter default must be a scalar", raw.Default.Line)
		}
		value := raw.Default.Value
		p.Default = &value
	}
	return nil
}

var (
	paramNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	paramRefPattern  = regexp.MustCompile(`\$\{\{\s*params\.([^}\s]*)\s*\}\}`)
)

// checkValue reports whether value is valid for the parameter.
func (p *Param) checkValue(name, value string) error {
	if len(value) > MaxParamValueLength {
		return fmt.Errorf("parameter %q is longer than %d characters", name, MaxParamValueLength)
	}
	switch p.Type {
	case ParamInteger:
		if _, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64); err != nil {
			return fmt.Errorf("parameter %q must be an integer, got %q", name, value)
		}
	case ParamNumber:
		if _, err := strconv.ParseFloat(strings.TrimSpace(value), 64); err != nil {
			return fmt.Errorf("parameter %q must be a number, got %q", name, value)
		}
	case ParamBoolean:
		if value != "true" && value != "false" {
			return fmt.Errorf("parameter %q must be true or false, got %q", name, value)
		}
	}
	if len(p.Values) > 0 {
		for _, allowed := range p.Values {
			if allowed == value {
				return nil
			}
		}
		return fmt.Errorf("parameter %q must be one of %s, got %q", name, strings.Join(p.Values, ", "), value)
	}
	return nil
}

func (p *Pipeline) validateParams() error {
	if len(p.Params) > MaxParams {
		return fmt.Errorf("at most %d params are allowed", MaxParams)
	}
	for _, name := range p.ParamNames() {
		param := p.Params[name]
		if param == nil {
			return fmt.Errorf("param %q is empty", name)
		}
		if !paramNamePattern.MatchString(name) {
			return fmt.Errorf("param %q must start with a letter or underscore and contain only letters, digits, and underscores", name)
		}
		if param.Type == "" {
			param.Type = ParamString
		}
		switch param.Type {
		case ParamString, ParamInteger, ParamNumber, ParamBoolean:
		default:
			return fmt.Errorf("param %q has unknown type %q (want string, integer, number, or boolean)", name, param.Type)
		}
		if param.Required && param.Default != nil {
			return fmt.Errorf("param %q is required and may not have a default", name)
		}
		seen := map[string]bool{}
		for _, value := range param.Values {
			if seen[value] {
				return fmt.Errorf("param %q lists value %q more than once", name, value)
			}
			seen[value] = true
			if err := (&Param{Type: param.Type}).checkValue(name, value); err != nil {
				return fmt.Errorf("param %q values: %w", name, err)
			}
		}
		if param.Default != nil {
			if err := param.checkValue(name, *param.Default); err != nil {
				return fmt.Errorf("default: %w", err)
			}
		}
	}
	for _, field := range p.paramFields() {
		for _, match := range paramRefPattern.FindAllStringSubmatch(*field.value, -1) {
			if _, ok := p.Params[match[1]]; !ok {
				return fmt.Errorf("%s references unknown param %q", field.where, match[1])
			}
		}
	}
	return nil
}

// ParamNames returns the declared parameter names in sorted order.
func (p *Pipeline) ParamNames() []string {
	names := make([]string, 0, len(p.Params))
	for name := range p.Params {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

type paramField struct {
	where string
	value *string
}

// paramFields lists every field that may reference ${{ params.name }}.
func (p *Pipeline) paramFields() []paramField {
	fields := []paramField{{"description", &p.Description}}
	for i := range p.Nodes {
		n := &p.Nodes[i]
		where := fmt.Sprintf("agent %q", n.ID)
		fields = append(fields,
			paramField{where + " description", &n.Description},
			paramField{where + " prompt", &n.Prompt},
			paramField{where + " model", &n.Model},
		)
		for j := range n.Handoff {
			fields = append(fields, paramField{fmt.Sprintf("%s handoff to %q input", where, n.Handoff[j].To), &n.Handoff[j].Input})
		}
		for j := range n.Stages {
			fields = append(fields, paramField{fmt.Sprintf("%s stage %q prompt", where, n.Stages[j].ID), &n.Stages[j].Prompt})
		}
		if n.Chunks != nil {
			for j := range n.Chunks.Initial {
				fields = append(fields, paramField{where + " chunk title", &n.Chunks.Initial[j]})
			}
		}
	}
	return fields
}

// ResolveParams applies defaults to the supplied values and checks them
// against the declarations. Unknown names, missing required values, and
// values of the wrong type are errors.
func (p *Pipeline) ResolveParams(values map[string]string) (map[string]string, error) {
	resolved := map[string]string{}
	for name, value := range values {
		if _, ok := p.Params[name]; !ok {
			if len(p.Params) == 0 {
				return nil, fmt.Errorf("pipeline %q does not declare any params (got %q)", p.Name, name)
			}
			return nil, fmt.Errorf("pipeline %q has no param %q (declared: %s)", p.Name, name, strings.Join(p.ParamNames(), ", "))
		}
		resolved[name] = value
	}
	var missing []string
	for _, name := range p.ParamNames() {
		param := p.Params[name]
		value, supplied := resolved[name]
		if !supplied && param.Default != nil {
			value = *param.Default
		}
		if param.Required && strings.TrimSpace(value) == "" {
			missing = append(missing, name)
			continue
		}
		// An empty optional value means "unset" and skips type checks.
		if value != "" {
			if err := param.checkValue(name, value); err != nil {
				return nil, err
			}
		}
		resolved[name] = value
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("pipeline %q requires param(s): %s", p.Name, strings.Join(missing, ", "))
	}
	if len(resolved) == 0 {
		return nil, nil
	}
	return resolved, nil
}

// RequiredParamsWithoutDefault lists params that a caller must supply.
func (p *Pipeline) RequiredParamsWithoutDefault() []string {
	var names []string
	for _, name := range p.ParamNames() {
		if p.Params[name].Required {
			names = append(names, name)
		}
	}
	return names
}

// WithParams resolves values (see ResolveParams) and returns a validated copy
// of the pipeline with every ${{ params.name }} reference substituted. The
// receiver is not modified. Pipelines without params are returned as-is.
func (p *Pipeline) WithParams(values map[string]string) (*Pipeline, map[string]string, error) {
	resolved, err := p.ResolveParams(values)
	if err != nil {
		return nil, nil, err
	}
	if len(p.Params) == 0 {
		return p, nil, nil
	}
	clone := p.clone()
	for _, field := range clone.paramFields() {
		*field.value = paramRefPattern.ReplaceAllStringFunc(*field.value, func(ref string) string {
			return resolved[paramRefPattern.FindStringSubmatch(ref)[1]]
		})
	}
	if err := clone.validate(); err != nil {
		return nil, nil, fmt.Errorf("after applying params: %w", err)
	}
	return clone, resolved, nil
}

// clone deep-copies the parts of a pipeline that parameter substitution or
// validation may modify.
func (p *Pipeline) clone() *Pipeline {
	c := *p
	c.Nodes = make([]Node, len(p.Nodes))
	for i, n := range p.Nodes {
		n.Handoff = append([]HandoffRule(nil), n.Handoff...)
		n.Stages = append([]Stage(nil), n.Stages...)
		if n.Chunks != nil {
			chunks := *n.Chunks
			chunks.Initial = append([]string(nil), n.Chunks.Initial...)
			n.Chunks = &chunks
		}
		c.Nodes[i] = n
	}
	c.byID = nil
	c.joins = nil
	c.scopes = nil
	return &c
}

// stripParamRefs removes ${{ params.name }} references so that other
// template syntax checks ignore them.
func stripParamRefs(s string) string {
	return paramRefPattern.ReplaceAllString(s, "")
}
