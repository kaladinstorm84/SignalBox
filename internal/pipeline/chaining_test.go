package pipeline

import (
	"strings"
	"testing"
)

const chainChild = `
name: child
params:
  topic: { required: true }
  tone: { default: plain }
agents:
  - id: a
    runtime: mock
    prompt: ${{ params.topic }}
    handoff: [b]
  - id: b
    runtime: mock
    handoff: [c]
  - id: c
    runtime: mock
    additional-input: [a]
    handoff: [d]
  - id: d
    runtime: mock
`

func parseAll(t *testing.T, yamls ...string) map[string]*Pipeline {
	t.Helper()
	out := map[string]*Pipeline{}
	for _, y := range yamls {
		p, err := Parse([]byte(y))
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		out[p.Name] = p
	}
	return out
}

func parentWith(node string) string {
	return "name: parent\nagents:\n  - id: sub\n    type: pipeline\n" + node
}

func TestPipelineNodeParsing(t *testing.T) {
	p, err := Parse([]byte(parentWith("    pipeline: child\n    from: b\n    until: c\n    params: { topic: x }\n    retry: { count: 1 }\n")))
	if err != nil {
		t.Fatal(err)
	}
	n := p.Node("sub")
	if n.Type != TypePipeline || n.Pipeline != "child" || n.From != "b" || n.Until != "c" || n.Params["topic"] != "x" || n.Retry.Count != 1 {
		t.Fatalf("node = %+v", n)
	}
}

func TestPipelineNodeValidationErrors(t *testing.T) {
	cases := map[string]string{
		"is missing pipeline":              parentWith("    from: b\n"),
		"runtime":                          parentWith("    pipeline: child\n    runtime: mock\n"),
		"prompt":                           parentWith("    pipeline: child\n    prompt: hi\n"),
		"not a valid param name":           parentWith("    pipeline: child\n    params: { \"bad name\": x }\n"),
		"only supported on pipeline nodes": "name: p\nagents:\n  - id: a\n    runtime: mock\n    pipeline: child\n",
	}
	for want, y := range cases {
		_, err := Parse([]byte(y))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("want error containing %q, got %v", want, err)
		}
	}
}

func TestCheckReferences(t *testing.T) {
	cases := map[string]struct {
		node string
		want string
	}{
		"ok":                {"    pipeline: child\n    params: { topic: x }\n", ""},
		"ok section":        {"    pipeline: child\n    from: a\n    until: c\n    params: { topic: x }\n", ""},
		"unknown":           {"    pipeline: nope\n", `unknown pipeline "nope"`},
		"bad from":          {"    pipeline: child\n    from: zz\n    params: { topic: x }\n", `from "zz" is not an agent`},
		"bad until":         {"    pipeline: child\n    until: zz\n    params: { topic: x }\n", `until "zz" is not an agent`},
		"unreachable until": {"    pipeline: child\n    from: c\n    until: b\n    params: { topic: x }\n", `until "b" is not reachable from "c"`},
		"outside input":     {"    pipeline: child\n    from: b\n    params: { topic: x }\n", `additional input from "a" outside the section`},
		"undeclared param":  {"    pipeline: child\n    params: { topic: x, mood: y }\n", `param "mood", which pipeline "child" does not declare`},
		"missing required":  {"    pipeline: child\n", `must pass param "topic"`},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			errs := CheckReferences(parseAll(t, chainChild, parentWith(tc.node)))
			if tc.want == "" {
				if len(errs) != 0 {
					t.Fatalf("unexpected errors: %v", errs)
				}
				return
			}
			if len(errs) != 1 || !strings.Contains(errs[0].Error(), tc.want) || !strings.Contains(errs[0].Error(), `agent "sub"`) {
				t.Fatalf("want %q, got %v", tc.want, errs)
			}
		})
	}
}

func TestCheckReferencesDetectsCycles(t *testing.T) {
	errs := CheckReferences(parseAll(t,
		"name: a\nagents:\n  - id: x\n    type: pipeline\n    pipeline: b\n",
		"name: b\nagents:\n  - id: y\n    type: pipeline\n    pipeline: a\n",
	))
	if len(errs) != 1 || !strings.Contains(errs[0].Error(), "pipeline reference cycle: a -> b -> a") {
		t.Fatalf("errs = %v", errs)
	}
	errs = CheckReferences(parseAll(t, "name: self\nagents:\n  - id: x\n    type: pipeline\n    pipeline: self\n"))
	if len(errs) != 1 || !strings.Contains(errs[0].Error(), "self -> self") {
		t.Fatalf("self errs = %v", errs)
	}
}

func TestLoadDirReportsReferenceErrors(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"parent.yaml": parentWith("    pipeline: missing\n"),
	})
	_, errs := LoadDir(dir)
	if len(errs) != 1 || !strings.Contains(errs[0].Error(), `unknown pipeline "missing"`) {
		t.Fatalf("errs = %v", errs)
	}
}

func TestSectionStopsAtUntil(t *testing.T) {
	p := parseAll(t, chainChild)["child"]
	got := keysOf(p.Section("b", "c"))
	if strings.Join(got, ",") != "b,c" {
		t.Fatalf("section = %v", got)
	}
}

func TestNodeParamsSubstitution(t *testing.T) {
	p, err := Parse([]byte(`
name: parent
params:
  subject: {}
agents:
  - id: sub
    type: pipeline
    pipeline: child
    params: { topic: "about ${{ params.subject }}" }
`))
	if err != nil {
		t.Fatal(err)
	}
	q, _, err := p.WithParams(map[string]string{"subject": "trains"})
	if err != nil {
		t.Fatal(err)
	}
	if got := q.Node("sub").Params["topic"]; got != "about trains" {
		t.Fatalf("substituted = %q", got)
	}
	if got := p.Node("sub").Params["topic"]; got != "about ${{ params.subject }}" {
		t.Fatalf("original mutated: %q", got)
	}
	if _, err := Parse([]byte(parentWith("    pipeline: child\n    params: { topic: \"${{ params.nope }}\" }\n"))); err == nil || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("undeclared ref err = %v", err)
	}
}
