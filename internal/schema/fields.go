package schema

import (
	"encoding/json"
	"fmt"
	"math/big"
	"reflect"
	"regexp"
	"strconv"
	"strings"
)

// OutputPrefix starts a field reference into a node's structured output.
const OutputPrefix = "output."

var segmentPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]*$|^[0-9]+$`)

// ParseFieldRef parses "output.a.b.0" into its path segments. ok is false
// when ref does not start with output.
func ParseFieldRef(ref string) (path []string, ok bool, err error) {
	ref = strings.TrimSpace(ref)
	if !strings.HasPrefix(ref, OutputPrefix) {
		return nil, false, nil
	}
	path = strings.Split(strings.TrimPrefix(ref, OutputPrefix), ".")
	for _, seg := range path {
		if !segmentPattern.MatchString(seg) {
			return nil, true, fmt.Errorf("invalid field path %q (use output.name.child with letters, digits, _ or -, or numeric array indexes)", ref)
		}
	}
	return path, true, nil
}

// Lookup follows a field path through decoded JSON.
func Lookup(doc any, path []string) (any, bool) {
	cur := doc
	for _, seg := range path {
		switch v := cur.(type) {
		case map[string]any:
			next, ok := v[seg]
			if !ok {
				return nil, false
			}
			cur = next
		case []any:
			i, err := strconv.Atoi(seg)
			if err != nil || i < 0 || i >= len(v) {
				return nil, false
			}
			cur = v[i]
		default:
			return nil, false
		}
	}
	return cur, true
}

// Render formats a looked-up value for a text template: strings verbatim,
// everything else as compact JSON, and missing values as empty.
func Render(v any, found bool) string {
	if !found || v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	encoded, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(encoded)
}

// Condition is a field comparison such as output.risk == "high".
type Condition struct {
	Path  []string
	Op    string
	Value any
}

// IsFieldCondition reports whether a handoff condition is a field comparison.
func IsFieldCondition(s string) bool {
	return strings.HasPrefix(strings.TrimSpace(s), OutputPrefix)
}

// ParseCondition parses `output.path == literal` or `output.path != literal`,
// where literal is a JSON string, number, true, false, or null.
func ParseCondition(s string) (*Condition, error) {
	s = strings.TrimSpace(s)
	var op string
	idx := -1
	for _, candidate := range []string{"==", "!="} {
		if i := strings.Index(s, candidate); i >= 0 && (idx < 0 || i < idx) {
			idx, op = i, candidate
		}
	}
	if idx < 0 {
		return nil, fmt.Errorf("condition %q must compare a field with == or != (for example output.risk == \"high\")", s)
	}
	path, ok, err := ParseFieldRef(s[:idx])
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("condition %q must start with output.", s)
	}
	literal := strings.TrimSpace(s[idx+len(op):])
	if strings.HasPrefix(literal, "'") && strings.HasSuffix(literal, "'") && len(literal) >= 2 {
		literal = strconv.Quote(literal[1 : len(literal)-1])
	}
	value, err := Parse(literal)
	if err != nil || isContainer(value) {
		return nil, fmt.Errorf("condition %q: right-hand side must be a quoted string, number, true, false, or null", s)
	}
	return &Condition{Path: path, Op: op, Value: value}, nil
}

func isContainer(v any) bool {
	switch v.(type) {
	case map[string]any, []any:
		return true
	}
	return false
}

// Eval tests the condition against a decoded document.
func (c *Condition) Eval(doc any) bool {
	actual, found := Lookup(doc, c.Path)
	if !found {
		actual = nil
	}
	equal := valuesEqual(actual, c.Value)
	if c.Op == "!=" {
		return !equal
	}
	return equal
}

func valuesEqual(a, b any) bool {
	ra, aNum := toRat(a)
	rb, bNum := toRat(b)
	if aNum || bNum {
		return aNum && bNum && ra.Cmp(rb) == 0
	}
	return reflect.DeepEqual(a, b)
}

func toRat(v any) (*big.Rat, bool) {
	var s string
	switch n := v.(type) {
	case json.Number:
		s = n.String()
	case float64:
		s = strconv.FormatFloat(n, 'g', -1, 64)
	case int:
		s = strconv.Itoa(n)
	default:
		return nil, false
	}
	r, ok := new(big.Rat).SetString(s)
	return r, ok
}
