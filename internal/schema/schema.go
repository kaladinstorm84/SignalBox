// Package schema loads the project's JSON Schema files and validates
// structured agent output against them.
package schema

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"golang.org/x/text/language"
	"golang.org/x/text/message"
	"gopkg.in/yaml.v3"
)

// MaxErrors bounds how many validation messages are reported per attempt.
const MaxErrors = 20

// Schema is one compiled schema file.
type Schema struct {
	Name string `json:"name"`
	// File is the schema's file name inside the schemas directory.
	File string `json:"file"`
	// Path is the schema file's absolute path.
	Path string `json:"-"`
	// Doc is the schema document as generic JSON values.
	Doc any `json:"schema"`
	// JSON is the schema document re-encoded as JSON.
	JSON []byte `json:"-"`

	compiled *jsonschema.Schema
}

// Set is the schemas in one directory, keyed by file name without extension.
type Set struct {
	Dir     string
	schemas map[string]*Schema
	errs    map[string]error
}

// Extensions lists the schema file extensions.
var Extensions = []string{".json", ".yaml", ".yml"}

// LoadDir loads every schema file in dir. A missing directory yields an
// empty set. Files that fail to parse or compile are reported in the errors
// and recorded so lookups by name explain the failure.
func LoadDir(dir string) (*Set, []error) {
	set := &Set{Dir: dir, schemas: map[string]*Schema{}, errs: map[string]error{}}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return set, nil
		}
		return set, []error{err}
	}
	var errs []error
	for _, e := range entries {
		if e.IsDir() || !isSchemaExt(strings.ToLower(filepath.Ext(e.Name()))) {
			continue
		}
		name := strings.TrimSuffix(e.Name(), filepath.Ext(e.Name()))
		if prev, dup := set.schemas[name]; dup {
			err := fmt.Errorf("schema %q is defined by both %s and %s", name, prev.File, e.Name())
			set.errs[name] = err
			errs = append(errs, err)
			continue
		}
		s, err := loadFile(filepath.Join(dir, e.Name()), name)
		if err != nil {
			err = fmt.Errorf("schema %s: %w", e.Name(), err)
			set.errs[name] = err
			errs = append(errs, err)
			continue
		}
		set.schemas[name] = s
	}
	for name := range set.errs {
		delete(set.schemas, name)
	}
	return set, errs
}

func isSchemaExt(ext string) bool {
	for _, x := range Extensions {
		if ext == x {
			return true
		}
	}
	return false
}

// Get returns the named schema.
func (s *Set) Get(name string) (*Schema, error) {
	if s == nil {
		return nil, fmt.Errorf("schema %q is not defined (no schemas directory)", name)
	}
	if err := s.errs[name]; err != nil {
		return nil, err
	}
	sch, ok := s.schemas[name]
	if !ok {
		return nil, fmt.Errorf("schema %q is not defined in %s", name, s.Dir)
	}
	return sch, nil
}

// List returns every valid schema, sorted by name.
func (s *Set) List() []*Schema {
	if s == nil {
		return nil
	}
	out := make([]*Schema, 0, len(s.schemas))
	for _, sch := range s.schemas {
		out = append(out, sch)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func loadFile(path, name string) (*Schema, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	s, err := Compile(name, data, strings.ToLower(filepath.Ext(path)) != ".json")
	if err != nil {
		return nil, err
	}
	s.Path = abs
	s.File = filepath.Base(path)
	return s, nil
}

// Compile parses and compiles a schema document. YAML documents are
// converted to JSON first.
func Compile(name string, data []byte, isYAML bool) (*Schema, error) {
	if isYAML {
		var doc any
		if err := yaml.Unmarshal(data, &doc); err != nil {
			return nil, fmt.Errorf("invalid YAML: %w", err)
		}
		converted, err := json.Marshal(doc)
		if err != nil {
			return nil, fmt.Errorf("YAML schema cannot be represented as JSON: %w", err)
		}
		data = converted
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}
	if _, ok := doc.(map[string]any); !ok {
		return nil, fmt.Errorf("a schema must be a JSON object")
	}
	url := "signalbox:///schemas/" + name + ".json"
	c := jsonschema.NewCompiler()
	c.DefaultDraft(jsonschema.Draft2020)
	// Remote $refs are never fetched.
	c.UseLoader(noLoader{})
	if err := c.AddResource(url, doc); err != nil {
		return nil, err
	}
	compiled, err := c.Compile(url)
	if err != nil {
		return nil, fmt.Errorf("invalid schema: %w", err)
	}
	encoded, err := json.Marshal(doc)
	if err != nil {
		return nil, err
	}
	return &Schema{Name: name, Doc: doc, JSON: encoded, compiled: compiled}, nil
}

type noLoader struct{}

func (noLoader) Load(url string) (any, error) {
	return nil, fmt.Errorf("external schema references are not supported (%s)", url)
}

var printer = message.NewPrinter(language.English)

// Validate checks a decoded JSON value and returns readable error messages;
// nil means valid.
func (s *Schema) Validate(v any) []string {
	err := s.compiled.Validate(v)
	if err == nil {
		return nil
	}
	ve, ok := err.(*jsonschema.ValidationError)
	if !ok {
		return []string{err.Error()}
	}
	var out []string
	var walk func(e *jsonschema.ValidationError)
	walk = func(e *jsonschema.ValidationError) {
		if len(out) >= MaxErrors {
			return
		}
		if len(e.Causes) == 0 {
			loc := "/" + strings.Join(e.InstanceLocation, "/")
			out = append(out, fmt.Sprintf("at %s: %s", loc, e.ErrorKind.LocalizedString(printer)))
			return
		}
		for _, c := range e.Causes {
			walk(c)
		}
	}
	walk(ve)
	if len(out) == 0 {
		out = []string{ve.Error()}
	}
	return out
}

// Result is the outcome of checking one piece of agent output.
type Result struct {
	// Value is the decoded document, when one was found.
	Value any
	// JSON is the document re-encoded with two-space indentation.
	JSON string
	// Errors is empty when the output is valid.
	Errors []string
}

// Check extracts a JSON document from text and validates it.
func (s *Schema) Check(text string) Result {
	v, err := Extract(text)
	if err != nil {
		return Result{Errors: []string{err.Error()}}
	}
	pretty, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return Result{Errors: []string{err.Error()}}
	}
	return Result{Value: v, JSON: string(pretty), Errors: s.Validate(v)}
}

// Extract finds the JSON document in agent output: the whole text, else the
// first fenced code block that parses, else the first {...} or [...] value.
func Extract(text string) (any, error) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return nil, fmt.Errorf("output is empty; expected a JSON document")
	}
	if v, err := decodeOne(trimmed); err == nil {
		return v, nil
	}
	for _, block := range fencedBlocks(trimmed) {
		if v, err := decodeOne(block); err == nil {
			return v, nil
		}
	}
	for i := 0; i < len(trimmed); i++ {
		if trimmed[i] != '{' && trimmed[i] != '[' {
			continue
		}
		dec := json.NewDecoder(strings.NewReader(trimmed[i:]))
		dec.UseNumber()
		var v any
		if err := dec.Decode(&v); err == nil {
			return v, nil
		}
	}
	return nil, fmt.Errorf("no JSON document found in output")
}

// Parse decodes text that must be exactly one JSON value.
func Parse(text string) (any, error) {
	return decodeOne(strings.TrimSpace(text))
}

func decodeOne(text string) (any, error) {
	dec := json.NewDecoder(strings.NewReader(text))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	if dec.More() {
		return nil, fmt.Errorf("trailing data after JSON value")
	}
	var extra any
	if err := dec.Decode(&extra); err == nil {
		return nil, fmt.Errorf("more than one JSON value")
	}
	return v, nil
}

func fencedBlocks(text string) []string {
	var blocks []string
	for rest := text; ; {
		start := strings.Index(rest, "```")
		if start < 0 {
			return blocks
		}
		body := rest[start+3:]
		nl := strings.IndexByte(body, '\n')
		if nl < 0 {
			return blocks
		}
		body = body[nl+1:]
		end := strings.Index(body, "```")
		if end < 0 {
			return blocks
		}
		blocks = append(blocks, strings.TrimSpace(body[:end]))
		rest = body[end+3:]
	}
}

// Instructions is the prompt text asking an agent for conforming JSON.
func (s *Schema) Instructions() string {
	pretty, err := json.MarshalIndent(s.Doc, "", "  ")
	if err != nil {
		pretty = s.JSON
	}
	return "Respond with only a single JSON document (no prose, no code fences) that conforms to this JSON Schema (" + s.Name + "):\n" + string(pretty)
}

// RepairInput appends validation feedback to an agent's input so it can
// correct its previous response.
func RepairInput(input, schemaName, previous string, errs []string) string {
	var b strings.Builder
	b.WriteString(input)
	b.WriteString("\n\n---\nYour previous response did not match the required JSON schema \"")
	b.WriteString(schemaName)
	b.WriteString("\".\nValidation errors:\n")
	for _, e := range errs {
		b.WriteString("- ")
		b.WriteString(e)
		b.WriteByte('\n')
	}
	b.WriteString("\nPrevious response:\n")
	b.WriteString(Truncate(previous, 4000))
	b.WriteString("\n\nReturn only a corrected JSON document.")
	return b.String()
}

// Truncate shortens s to at most n bytes, marking the cut.
func Truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
