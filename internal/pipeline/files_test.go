package pipeline

import (
	"strings"
	"testing"
)

func TestFilesDeclarationAndNodeModes(t *testing.T) {
	p, err := Parse([]byte(`
name: up
files:
  required: true
  max_count: 2
  extensions: [CSV, .md]
params:
  main: {type: file}
agents:
  - id: a
    runtime: mock
    files: inline
    files_budget: 500
    handoff: [b]
  - id: b
    type: code
    language: python
    code-source: x.py
    files: path
    handoff: [c]
  - id: c
    type: pipeline
    pipeline: other
    files: inherit
    handoff: [d]
  - id: d
    runtime: mock
`))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(p.Files.Extensions, " ") != ".csv .md" {
		t.Fatalf("extensions = %v", p.Files.Extensions)
	}
	if got := strings.Join(p.FileParams(), ","); got != "main" {
		t.Fatalf("file params = %s", got)
	}
	modes := map[string]string{}
	for _, n := range p.Nodes {
		modes[n.ID] = n.FilesMode()
	}
	if modes["a"] != FilesInline || modes["b"] != FilesPath || modes["c"] != FilesInherit || modes["d"] != FilesNone {
		t.Fatalf("modes = %v", modes)
	}
	if p.Nodes[0].InlineBudget() != 500 || p.Nodes[3].InlineBudget() != DefaultFilesBudget {
		t.Fatal("inline budget")
	}
}

func TestFilesValidationErrors(t *testing.T) {
	cases := map[string]string{
		"bad mode":            "agents:\n  - id: a\n    runtime: mock\n    files: attach\n",
		"inherit on agent":    "agents:\n  - id: a\n    runtime: mock\n    files: inherit\n",
		"inline on pipeline":  "agents:\n  - id: a\n    type: pipeline\n    pipeline: x\n    files: inline\n",
		"approval":            "agents:\n  - id: a\n    type: approval\n    files: inline\n",
		"budget without mode": "agents:\n  - id: a\n    runtime: mock\n    files_budget: 10\n",
		"budget too big":      "agents:\n  - id: a\n    runtime: mock\n    files: inline\n    files_budget: 2000000\n",
		"bad extension":       "files: {extensions: [\"../x\"]}\nagents:\n  - id: a\n    runtime: mock\n",
		"dup extension":       "files: {extensions: [csv, .CSV]}\nagents:\n  - id: a\n    runtime: mock\n",
		"negative count":      "files: {max_count: -1}\nagents:\n  - id: a\n    runtime: mock\n",
		"file param default":  "params:\n  f: {type: file, default: a.csv}\nagents:\n  - id: a\n    runtime: mock\n",
	}
	for name, body := range cases {
		if _, err := Parse([]byte("name: p\n" + body)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
