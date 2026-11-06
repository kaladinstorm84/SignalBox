package pipeline

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaladinstorm84/SignalBox/internal/schema"
)

func schemaSet(t *testing.T) *schema.Set {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"verdict.json": `{"type":"object","required":["decision","feedback","score"],"properties":{"decision":{"enum":["approve","reject"]},"feedback":{"type":"string"},"score":{"type":"integer"}}}`,
		"triage.yaml":  "type: object\nrequired: [risk]\nproperties:\n  risk: {enum: [low, high]}\n",
		"broken.json":  `{"type":`,
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	set, _ := schema.LoadDir(dir)
	return set
}

func TestStructuredOutputValidation(t *testing.T) {
	set := schemaSet(t)
	base := "name: p\nentry: a\nagents:\n"
	cases := map[string]string{
		"wrong node type":       "  - id: a\n    type: approval\n    output: {schema: triage}\n",
		"missing name":          "  - id: a\n    runtime: mock\n    output: {on_invalid: fail}\n",
		"path in name":          "  - id: a\n    runtime: mock\n    output: {schema: ../triage}\n",
		"bad policy":            "  - id: a\n    runtime: mock\n    output: {schema: triage, on_invalid: ignore}\n",
		"too many repairs":      "  - id: a\n    runtime: mock\n    output: {schema: triage, max_repairs: 11}\n",
		"repairs with fail":     "  - id: a\n    runtime: mock\n    output: {schema: triage, on_invalid: fail, max_repairs: 1}\n",
		"code default fail":     "  - id: a\n    type: code\n    language: python\n    code-source: x.py\n    output: {schema: triage, max_repairs: 1}\n",
		"unknown schema":        "  - id: a\n    runtime: mock\n    output: {schema: nope}\n",
		"invalid schema":        "  - id: a\n    runtime: mock\n    output: {schema: broken}\n",
		"condition no schema":   "  - id: a\n    runtime: mock\n    handoff:\n      - {to: b, condition: 'output.risk == \"high\"'}\n  - id: b\n    runtime: mock\n",
		"bad condition":         "  - id: a\n    runtime: mock\n    output: {schema: triage}\n    handoff:\n      - {to: b, condition: 'output.risk > 3'}\n  - id: b\n    runtime: mock\n",
		"template no schema":    "  - id: a\n    runtime: mock\n    handoff:\n      - {to: b, input: '{{ output.risk }}'}\n  - id: b\n    runtime: mock\n",
		"bad template path":     "  - id: a\n    runtime: mock\n    output: {schema: triage}\n    handoff:\n      - {to: b, input: '{{ output.ri sk }}'}\n  - id: b\n    runtime: mock\n",
		"unknown input schema":  "  - id: a\n    runtime: mock\ninput: {schema: nope}\n",
		"input schema path":     "  - id: a\n    runtime: mock\ninput: {schema: a/b}\n",
		"unknown plain keyword": "  - id: a\n    runtime: mock\n    handoff:\n      - {to: b, condition: sometimes}\n  - id: b\n    runtime: mock\n",
	}
	for name, agents := range cases {
		if _, err := ParseWith([]byte(base+agents), Options{Schemas: set}); err == nil {
			t.Errorf("%s: parsed without error", name)
		}
	}

	ok := base + `  - id: a
    runtime: mock
    output: {schema: triage, max_repairs: 3}
    handoff:
      - {to: b, condition: 'output.risk == "high"', input: 'Risk {{ output.risk }} for {{ input }}'}
      - {to: c, condition: 'output.risk != "high"'}
  - id: b
    type: llm_gate
    runtime: mock
    prompt: judge
    output: {schema: verdict}
    handoff:
      - {to: c, condition: 'output.score == 5'}
  - id: c
    type: code
    language: python
    code-source: x.py
    output: {schema: triage}
input: {schema: triage}
`
	p, err := ParseWith([]byte(ok), Options{Schemas: set})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(p.SchemaNames(), ","); got != "triage,verdict" {
		t.Fatalf("schema names = %s", got)
	}
	if p.SchemaFor("triage") == nil || p.SchemaFor("verdict") == nil || p.InputSchema() != "triage" {
		t.Fatal("schemas not resolved")
	}
	a, c := p.Node("a"), p.Node("c")
	if a.InvalidPolicy() != OnInvalidRepair || a.RepairLimit() != 3 || c.InvalidPolicy() != OnInvalidFail || c.RepairLimit() != 0 {
		t.Fatalf("policies a=%s/%d c=%s/%d", a.InvalidPolicy(), a.RepairLimit(), c.InvalidPolicy(), c.RepairLimit())
	}
	if p.Node("b").RepairLimit() != DefaultMaxRepairs {
		t.Fatalf("gate repair default = %d", p.Node("b").RepairLimit())
	}
	clone, _, err := p.WithParams(nil)
	if err != nil || clone.SchemaFor("triage") == nil {
		t.Fatalf("clone lost schemas: %v", err)
	}
}

func TestSchemasLoadFromProjectDirectory(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{"pipelines", "schemas", "contracts"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write := func(rel, content string) {
		if err := os.WriteFile(filepath.Join(root, rel), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("schemas/triage.json", `{"type":"object"}`)
	write("contracts/other.json", `{"type":"object"}`)
	write("pipelines/a.yaml", "name: a\nentry: x\nagents:\n  - id: x\n    runtime: mock\n    output: {schema: triage}\n")
	write("pipelines/b.yaml", "name: b\nentry: x\nagents:\n  - id: x\n    runtime: mock\n    output: {schema: other}\n")

	// Default directory: Root/schemas.
	ps, errs := LoadDir(filepath.Join(root, "pipelines"), Options{Root: root})
	if len(errs) != 1 || !strings.Contains(errs[0].Error(), `"other"`) || ps["a"] == nil {
		t.Fatalf("default dir: %v", errs)
	}
	// Configured directory.
	ps, errs = LoadDir(filepath.Join(root, "pipelines"), Options{Root: root, SchemasDir: filepath.Join(root, "contracts")})
	if len(errs) != 1 || ps["b"] == nil || ps["a"] != nil {
		t.Fatalf("configured dir: %v", errs)
	}
	// Pipelines without schemas never need the directory.
	if _, err := Parse([]byte("name: p\nentry: x\nagents:\n  - id: x\n    runtime: mock\n")); err != nil {
		t.Fatal(err)
	}
	if _, err := Parse([]byte("name: p\nentry: x\nagents:\n  - id: x\n    runtime: mock\n    output: {schema: triage}\n")); err == nil {
		t.Fatal("schema resolved without a schemas directory")
	}
}
