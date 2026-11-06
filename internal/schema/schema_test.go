package schema

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const invoiceSchema = `{
  "type": "object",
  "required": ["total", "risk", "lines"],
  "properties": {
    "total": {"type": "number", "minimum": 0},
    "risk": {"type": "string", "enum": ["low", "high"]},
    "customer": {"$ref": "#/$defs/customer"},
    "lines": {"type": "array", "minItems": 2, "items": {"type": "object", "required": ["sku"], "properties": {"sku": {"type": "string", "minLength": 10}}}}
  },
  "$defs": {"customer": {"type": "object", "properties": {"email": {"type": "string", "format": "email"}}}}
}`

func writeFiles(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestLoadDirJSONYAMLAndErrors(t *testing.T) {
	dir := writeFiles(t, map[string]string{
		"invoice.json": invoiceSchema,
		"risk.yaml":    "type: object\nrequired: [level]\nproperties:\n  level: {type: integer, maximum: 5}\n",
		"broken.json":  `{"type": "object",`,
		"badtype.json": `{"type": "nonsense"}`,
		"dup.json":     `{"type": "string"}`,
		"dup.yml":      "type: string\n",
		"notes.txt":    "ignored",
	})
	set, errs := LoadDir(dir)
	if len(errs) != 3 {
		t.Fatalf("errors = %v", errs)
	}
	names := []string{}
	for _, s := range set.List() {
		names = append(names, s.Name)
	}
	if strings.Join(names, ",") != "invoice,risk" {
		t.Fatalf("names = %v", names)
	}
	for _, bad := range []string{"broken", "badtype", "dup", "missing"} {
		if _, err := set.Get(bad); err == nil {
			t.Fatalf("Get(%q) succeeded", bad)
		}
	}
	risk, err := set.Get("risk")
	if err != nil {
		t.Fatal(err)
	}
	if errs := risk.Check(`{"level": 3}`).Errors; len(errs) != 0 {
		t.Fatalf("valid YAML schema doc rejected: %v", errs)
	}
	if errs := risk.Check(`{"level": 9}`).Errors; len(errs) != 1 || !strings.Contains(errs[0], "/level") {
		t.Fatalf("errors = %v", errs)
	}
	if empty, errs := LoadDir(filepath.Join(dir, "absent")); len(errs) != 0 || len(empty.List()) != 0 {
		t.Fatalf("missing dir: %v %v", empty.List(), errs)
	}
}

func TestExternalRefsAreRejected(t *testing.T) {
	if _, err := Compile("x", []byte(`{"$ref": "https://example.com/schema.json"}`), false); err == nil {
		t.Fatal("remote $ref compiled")
	}
}

func TestExtract(t *testing.T) {
	cases := map[string]string{
		"plain":    `{"a": 1}`,
		"fenced":   "Here you go:\n```json\n{\"a\": 1}\n```\nThanks",
		"embedded": "Result: {\"a\": 1} -- done",
		"padded":   "\n  {\"a\": 1}  \n",
	}
	for name, text := range cases {
		v, err := Extract(text)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got := Render(Lookup(v, []string{"a"})); got != "1" {
			t.Fatalf("%s: a = %q", name, got)
		}
	}
	for _, bad := range []string{"", "no json here", "{broken"} {
		if _, err := Extract(bad); err == nil {
			t.Fatalf("Extract(%q) succeeded", bad)
		}
	}
	if _, err := Parse(`{"a":1} trailing`); err == nil {
		t.Fatal("Parse accepted trailing data")
	}
}

func TestCheckReportsErrorsAndNormalizes(t *testing.T) {
	sch, err := Compile("invoice", []byte(invoiceSchema), false)
	if err != nil {
		t.Fatal(err)
	}
	res := sch.Check("```json\n{\"total\": -1, \"risk\": \"medium\"}\n```")
	if len(res.Errors) < 3 {
		t.Fatalf("errors = %v", res.Errors)
	}
	joined := strings.Join(res.Errors, "\n")
	for _, want := range []string{"/total", "/risk", "lines"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("errors %q missing %q", joined, want)
		}
	}
	ok := sch.Check(`{"total": 12.5, "risk": "low", "lines": [{"sku": "ABCDEFGHIJ"}, {"sku": "KLMNOPQRST"}]}`)
	if len(ok.Errors) != 0 || !strings.Contains(ok.JSON, "\n  \"lines\"") {
		t.Fatalf("result = %+v", ok)
	}
}

func TestSampleConforms(t *testing.T) {
	schemas := []string{
		invoiceSchema,
		`{"type": "object", "required": ["n", "tags", "kind"], "properties": {"n": {"type": "integer", "exclusiveMinimum": 3, "maximum": 10}, "tags": {"type": "array", "items": {"type": "string"}, "minItems": 3}, "kind": {"oneOf": [{"const": "a"}, {"const": "b"}]}, "when": {"type": "string", "format": "date-time"}}}`,
		`{"allOf": [{"type": "object", "properties": {"a": {"type": "boolean"}}, "required": ["a"]}, {"type": "object", "properties": {"b": {"type": ["null", "string"], "maxLength": 3}}, "required": ["b"]}]}`,
		`{"type": "array", "items": {"type": "number", "minimum": 5}, "maxItems": 1}`,
	}
	for i, src := range schemas {
		sch, err := Compile("s", []byte(src), false)
		if err != nil {
			t.Fatalf("%d: %v", i, err)
		}
		if errs := sch.Check(sch.Sample()).Errors; len(errs) != 0 {
			t.Fatalf("%d: sample %s invalid: %v", i, sch.Sample(), errs)
		}
	}
}

func TestConditions(t *testing.T) {
	doc, err := Parse(`{"risk": "high", "score": 0.80, "flags": {"urgent": true}, "items": [{"id": "x"}], "none": null}`)
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]bool{
		`output.risk == "high"`:        true,
		`output.risk != "high"`:        false,
		`output.risk == 'high'`:        true,
		`output.score == 0.8`:          true,
		`output.score != 1`:            true,
		`output.flags.urgent == true`:  true,
		`output.items.0.id == "x"`:     true,
		`output.none == null`:          true,
		`output.missing == null`:       true,
		`output.missing != "x"`:        true,
		`output.score == "0.8"`:        false,
		`output.risk == "a==b"`:        false,
		`output.risk != "a!=b"`:        true,
		`output.items.5.id == "x"`:     false,
		`output.flags.urgent == false`: false,
	}
	for src, want := range cases {
		cond, err := ParseCondition(src)
		if err != nil {
			t.Fatalf("%s: %v", src, err)
		}
		if got := cond.Eval(doc); got != want {
			t.Fatalf("%s = %v, want %v", src, got, want)
		}
	}
	for _, bad := range []string{`output.risk`, `output.risk == high`, `output.risk == {"a":1}`, `output..x == 1`, `input.risk == "a"`, `output.a b == 1`} {
		if _, err := ParseCondition(bad); err == nil {
			t.Fatalf("ParseCondition(%q) succeeded", bad)
		}
	}
	if got := Render(Lookup(doc, []string{"flags"})); got != `{"urgent":true}` {
		t.Fatalf("render object = %q", got)
	}
	if got := Render(Lookup(doc, []string{"absent"})); got != "" {
		t.Fatalf("render missing = %q", got)
	}
}
