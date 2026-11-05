package pipeline

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFiles(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

const sharedLibrary = `templates:
  - id: base
    runtime: mock
    retry: { count: 2, backoff: 1s }
    context: { strategy: recent, window: 2 }
  - id: reviewer
    extends: base
    type: llm_gate
    prompt: Review strictly.
agents:
  - id: security
    extends: reviewer
    prompt: Review for security issues.
  - id: unused
    runtime: mock
`

func TestImportsMergeSemantics(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, map[string]string{
		"shared/review.yaml": sharedLibrary,
		"pipelines/p.yaml": `name: p
imports:
  - from: ../shared/review.yaml
    agents: [security]
templates:
  - id: writer_base
    extends: base
    prompt: Write.
    context: { window: 3 }
agents:
  - id: writer
    extends: writer_base
    handoff: [security]
    retry: { count: 1 }
`,
	})
	p, err := Load(filepath.Join(root, "pipelines", "p.yaml"), Options{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	writer := p.Node("writer")
	if writer.Runtime != "mock" || writer.Prompt != "Write." {
		t.Fatalf("writer = %+v", writer)
	}
	// Maps deep-merge (backoff inherited), scalars in the child win.
	if writer.Retry.Count != 1 || writer.Retry.Backoff != "1s" {
		t.Fatalf("retry = %+v", writer.Retry)
	}
	if writer.Context.Strategy != ContextRecent || writer.Context.Window != 3 {
		t.Fatalf("context = %+v", writer.Context)
	}
	security := p.Node("security")
	if security == nil || security.Type != TypeLLMGate || security.Prompt != "Review for security issues." || security.Retry.Count != 2 {
		t.Fatalf("security = %+v", security)
	}
	if p.Node("unused") != nil {
		t.Fatal("unselected agents must not be imported")
	}
	if len(p.Nodes) != 2 || p.Nodes[0].ID != "writer" || p.Nodes[1].ID != "security" {
		t.Fatalf("local agents must precede imported ones: %v", []string{p.Nodes[0].ID, p.Nodes[1].ID})
	}
	if len(p.Sources) != 1 || p.Sources[0] != "shared/review.yaml" {
		t.Fatalf("sources = %v", p.Sources)
	}
}

func TestImportsDeterministic(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, map[string]string{
		"shared/a.yaml": "agents:\n  - id: a1\n    runtime: mock\n    handoff: [a2]\n  - id: a2\n    runtime: mock\n",
		"shared/b.yaml": "agents:\n  - id: b1\n    runtime: mock\n",
		"pipelines/p.yaml": `name: p
imports:
  - from: ../shared/b.yaml
  - from: ../shared/a.yaml
agents:
  - id: start
    runtime: mock
    handoff: [b1, a1]
`,
	})
	var first []string
	for i := 0; i < 5; i++ {
		p, err := Load(filepath.Join(root, "pipelines", "p.yaml"), Options{Root: root})
		if err != nil {
			t.Fatal(err)
		}
		var ids []string
		for _, n := range p.Nodes {
			ids = append(ids, n.ID)
		}
		if strings.Join(ids, ",") != "start,b1,a1,a2" {
			t.Fatalf("order = %v", ids)
		}
		if first == nil {
			first = ids
		}
	}
}

func TestImportErrors(t *testing.T) {
	cases := []struct {
		name  string
		files map[string]string
		opts  func(root string) Options
		want  string
	}{
		{
			name: "cycle",
			files: map[string]string{
				"shared/a.yaml":    "imports: [{from: b.yaml}]\ntemplates: [{id: ta, runtime: mock}]\n",
				"shared/b.yaml":    "imports: [{from: a.yaml}]\n",
				"pipelines/p.yaml": "name: p\nimports: [{from: ../shared/a.yaml}]\nagents: [{id: x, runtime: mock}]\n",
			},
			want: "import cycle: pipelines/p.yaml -> shared/a.yaml -> shared/b.yaml -> shared/a.yaml",
		},
		{
			name: "outside root",
			files: map[string]string{
				"pipelines/p.yaml": "name: p\nimports: [{from: ../../elsewhere.yaml}]\nagents: [{id: x, runtime: mock}]\n",
			},
			want: "outside the project root",
		},
		{
			name: "duplicate local and imported",
			files: map[string]string{
				"shared/a.yaml":    "agents: [{id: x, runtime: mock}]\n",
				"pipelines/p.yaml": "name: p\nimports: [{from: ../shared/a.yaml}]\nagents: [{id: x, runtime: mock}]\n",
			},
			want: `agent "x" is defined locally and imported from shared/a.yaml`,
		},
		{
			name: "duplicate across imports",
			files: map[string]string{
				"shared/a.yaml":    "agents: [{id: y, runtime: mock}]\n",
				"shared/b.yaml":    "agents: [{id: y, runtime: mock}]\n",
				"pipelines/p.yaml": "name: p\nimports: [{from: ../shared/a.yaml}, {from: ../shared/b.yaml}]\nagents: [{id: x, runtime: mock, handoff: [y]}]\n",
			},
			want: `agent "y" is imported from both shared/a.yaml and shared/b.yaml`,
		},
		{
			name: "duplicate template",
			files: map[string]string{
				"shared/a.yaml":    "templates: [{id: t, runtime: mock}]\n",
				"pipelines/p.yaml": "name: p\nimports: [{from: ../shared/a.yaml}]\ntemplates: [{id: t, runtime: mock}]\nagents: [{id: x, extends: t}]\n",
			},
			want: `template "t" is defined in both pipelines/p.yaml and shared/a.yaml`,
		},
		{
			name: "missing selected agent",
			files: map[string]string{
				"shared/a.yaml":    "agents: [{id: y, runtime: mock}]\n",
				"pipelines/p.yaml": "name: p\nimports: [{from: ../shared/a.yaml, agents: [z]}]\nagents: [{id: x, runtime: mock}]\n",
			},
			want: `has no agent "z"`,
		},
		{
			name: "unknown template",
			files: map[string]string{
				"pipelines/p.yaml": "name: p\nagents: [{id: x, extends: nope}]\n",
			},
			want: `extends unknown template "nope"`,
		},
		{
			name: "template cycle",
			files: map[string]string{
				"pipelines/p.yaml": "name: p\ntemplates: [{id: a, extends: b}, {id: b, extends: a}]\nagents: [{id: x, extends: a, runtime: mock}]\n",
			},
			want: "template extends cycle",
		},
		{
			name: "non yaml import",
			files: map[string]string{
				"pipelines/p.yaml": "name: p\nimports: [{from: ../x.json}]\nagents: [{id: x, runtime: mock}]\n",
			},
			want: "must be .yaml or .yml",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "project")
			writeFiles(t, root, c.files)
			_, err := Load(filepath.Join(root, "pipelines", "p.yaml"), Options{Root: root})
			if err == nil || !strings.Contains(filepath.ToSlash(err.Error()), c.want) {
				t.Fatalf("err = %v, want %q", err, c.want)
			}
		})
	}
}

func TestExternalImportsRequireOptIn(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "project")
	writeFiles(t, base, map[string]string{
		"lib/shared.yaml":          "agents: [{id: y, runtime: mock}]\n",
		"project/pipelines/p.yaml": "name: p\nimports: [{from: ../../lib/shared.yaml}]\nagents: [{id: x, runtime: mock, handoff: [y]}]\n",
	})
	path := filepath.Join(root, "pipelines", "p.yaml")
	if _, err := Load(path, Options{Root: root}); err == nil || !strings.Contains(err.Error(), "allow_external_imports") {
		t.Fatalf("err = %v", err)
	}
	p, err := Load(path, Options{Root: root, AllowExternal: true})
	if err != nil {
		t.Fatal(err)
	}
	if p.Node("y") == nil {
		t.Fatal("external agent missing")
	}
}

func TestParseRejectsImportsWithoutFile(t *testing.T) {
	_, err := Parse([]byte("name: p\nimports: [{from: x.yaml}]\nagents: [{id: x, runtime: mock}]\n"))
	if err == nil || !strings.Contains(err.Error(), "loaded from a file") {
		t.Fatalf("err = %v", err)
	}
	p, err := Parse([]byte("name: p\ntemplates: [{id: t, runtime: mock, prompt: hi}]\nagents: [{id: x, extends: t}]\n"))
	if err != nil {
		t.Fatal(err)
	}
	if p.Node("x").Prompt != "hi" {
		t.Fatal("in-file templates should work without a file")
	}
}

func TestImportDiamondIsNotDuplicate(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, map[string]string{
		"shared/base.yaml": "templates: [{id: t, runtime: mock}]\n",
		"shared/b.yaml":    "imports: [{from: base.yaml}]\n",
		"shared/c.yaml":    "imports: [{from: base.yaml}]\n",
		"pipelines/p.yaml": "name: p\nimports: [{from: ../shared/b.yaml}, {from: ../shared/c.yaml}]\nagents: [{id: x, extends: t}]\n",
	})
	p, err := Load(filepath.Join(root, "pipelines", "p.yaml"), Options{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	if p.Node("x").Runtime != "mock" {
		t.Fatal("template not applied")
	}
	if len(p.Sources) != 3 {
		t.Fatalf("sources = %v", p.Sources)
	}
}
