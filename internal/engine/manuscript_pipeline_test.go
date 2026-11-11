package engine

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/kaladinstorm84/SignalBox/internal/config"
	"github.com/kaladinstorm84/SignalBox/internal/pipeline"
	"github.com/kaladinstorm84/SignalBox/internal/runtime"
)

const editorialNotes = `{
  "focus": "continuity",
  "summary": "One name contradiction.",
  "strengths": [{"point": "Clear timeline", "location": "Chapter Three"}],
  "issues": [{
    "id": "CON-01", "severity": "moderate",
    "location": "Chapter One and Chapter Two",
    "evidence": "\"a border collie named Biscuit\" vs \"Pepper,\" said Maren",
    "why_it_matters": "Readers lose trust in the narrator.",
    "suggestion": "Choose one name."
  }],
  "questions_for_author": []
}`

func loadManuscriptPipeline(t *testing.T) (*pipeline.Pipeline, string) {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", "..", "testproject"))
	if err != nil {
		t.Fatal(err)
	}
	p, err := pipeline.Load(filepath.Join(root, "pipelines", "manuscript-review.yaml"), pipeline.Options{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	return p, root
}

func TestEditorialNotesSchema(t *testing.T) {
	p, _ := loadManuscriptPipeline(t)
	sch := p.SchemaFor("editorial-notes")
	if sch == nil {
		t.Fatal("editorial-notes schema is not loaded")
	}
	cases := []struct {
		name, document string
		valid          bool
	}{
		{"complete notes", editorialNotes, true},
		{"no issues", `{"focus":"continuity","summary":"None found.","strengths":[{"point":"Consistent","location":"Throughout"}],"issues":[],"questions_for_author":[]}`, true},
		{"unknown focus", strings.Replace(editorialNotes, `"continuity"`, `"vibes"`, 1), false},
		{"bad issue id", strings.Replace(editorialNotes, `"CON-01"`, `"CON-1"`, 1), false},
		{"bad severity", strings.Replace(editorialNotes, `"moderate"`, `"critical"`, 1), false},
		{"empty evidence", strings.Replace(editorialNotes, `"\"a border collie named Biscuit\" vs \"Pepper,\" said Maren"`, `""`, 1), false},
		{"no strengths", strings.Replace(editorialNotes, `[{"point": "Clear timeline", "location": "Chapter Three"}]`, `[]`, 1), false},
		{"extra field", strings.Replace(editorialNotes, `"summary"`, `"score": 7, "summary"`, 1), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			errors := sch.Check(tc.document).Errors
			if (len(errors) == 0) != tc.valid {
				t.Fatalf("valid = %v, want %v; errors = %v", len(errors) == 0, tc.valid, errors)
			}
		})
	}
}

func TestManuscriptReviewPipeline(t *testing.T) {
	if _, err := exec.LookPath("python"); err != nil {
		t.Skip("Python is required for this integration test")
	}
	p, root := loadManuscriptPipeline(t)
	clearSampleAIOptionsForMock(p)
	manuscript, err := os.ReadFile(filepath.Join(root, "samples", "manuscript-sample.md"))
	if err != nil {
		t.Fatal(err)
	}
	registry, err := runtime.NewRegistry(map[string]config.RuntimeConfig{"mock": {Type: "mock"}})
	if err != nil {
		t.Fatal(err)
	}
	// Prompts start with a parameterised line, so match each node by a phrase.
	phrases := map[string]string{
		"structure_editor":  "developmental editor assessing structure",
		"character_editor":  "developmental editor assessing character",
		"prose_editor":      "You are a line editor.",
		"continuity_editor": "You are a continuity editor.",
		"market_editor":     "acquisitions-minded editor",
		"editorial_letter":  "senior editor writing an editorial letter",
		"letter_check":      "You check an editorial letter",
	}
	var mu sync.Mutex
	calls := map[string][]runtime.Request{}
	handle := func(id string, req runtime.Request) (string, error) {
		calls[id] = append(calls[id], req)
		attempt := len(calls[id])
		if !strings.Contains(req.SystemPrompt, "literary mystery") && id != "letter_check" {
			return "", fmt.Errorf("%s did not receive the genre param", id)
		}
		if len(req.Files) != 1 || filepath.Base(req.Files[0]) != "manuscript-sample.md" {
			return "", fmt.Errorf("%s did not receive the manuscript path: %v", id, req.Files)
		}
		if data, err := os.ReadFile(req.Files[0]); err != nil || string(data) != string(manuscript) {
			return "", fmt.Errorf("%s cannot read the manuscript: %v", id, err)
		}
		switch id {
		case "structure_editor", "character_editor", "prose_editor", "continuity_editor", "market_editor":
			if req.Schema == nil {
				return "", fmt.Errorf("%s has no output schema", id)
			}
			if !strings.Contains(req.Input, "## Manuscript profile: manuscript-sample.md") ||
				!strings.Contains(req.Input, "Worried the middle drags") {
				return "", fmt.Errorf("%s did not receive the profile and brief", id)
			}
			if id == "continuity_editor" && attempt == 1 {
				return `{"focus":"continuity","summary":"Dog renamed."}`, nil
			}
			return strings.Replace(editorialNotes, `"continuity"`, fmt.Sprintf("%q", strings.TrimSuffix(id, "_editor")), 1), nil
		case "editorial_letter":
			if !strings.Contains(req.Input, "--- additional input from manuscript_profile ---\n## Author brief") ||
				strings.Count(req.Input, `"CON-01"`) != 5 {
				return "", fmt.Errorf("letter lost the profile or specialist notes")
			}
			switch attempt {
			case 2:
				if !strings.Contains(req.Input, "Cite where the dog is renamed") || !strings.Contains(req.Input, "Letter draft 1") {
					return "", fmt.Errorf("letter rework lost gate feedback or its draft")
				}
			case 3:
				if !strings.Contains(req.Input, "Soften the market section") || !strings.Contains(req.Input, "Letter draft 2") {
					return "", fmt.Errorf("letter revision lost editor feedback or its draft")
				}
			}
			return fmt.Sprintf("## Overall Assessment\nLetter draft %d\n## Continuity Notes\n| CON-01 | Biscuit / Pepper |", attempt), nil
		case "letter_check":
			if !strings.Contains(req.Input, "## Overall Assessment") {
				return "", fmt.Errorf("gate did not receive the letter")
			}
			if attempt == 1 {
				return `{"decision":"reject","feedback":"Cite where the dog is renamed"}`, nil
			}
			return `{"decision":"approve","feedback":"Evidence checks out"}`, nil
		}
		return "", fmt.Errorf("unexpected call: %s", id)
	}
	registry.Register(&runtime.Mock{RuntimeName: "codex", Handler: func(_ context.Context, req runtime.Request) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		for id, phrase := range phrases {
			if strings.Contains(req.SystemPrompt, phrase) {
				return handle(id, req)
			}
		}
		return "", fmt.Errorf("unrecognized prompt: %.80s", req.SystemPrompt)
	}})
	eng, err := NewWithLimit(map[string]*pipeline.Pipeline{p.Name: p}, registry, nil, 4, root)
	if err != nil {
		t.Fatal(err)
	}
	eng.filesRoot = t.TempDir()
	if _, err := eng.StartWith(StartOptions{Pipeline: p.Name, Files: []FileInput{{Name: "a.md", Data: manuscript}, {Name: "b.md", Data: manuscript}}}); err == nil {
		t.Fatal("two manuscripts were accepted")
	}
	if _, err := eng.StartWith(StartOptions{Pipeline: p.Name}); err == nil {
		t.Fatal("a run without a manuscript was accepted")
	}
	id, err := eng.StartWith(StartOptions{
		Pipeline: p.Name,
		Input:    "First novel. Worried the middle drags.",
		Params:   map[string]string{"genre": "literary mystery", "edit_depth": "developmental"},
		Files:    []FileInput{{Name: "manuscript-sample.md", Data: manuscript}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = eng.Cancel(id) })
	t.Cleanup(func() {
		if t.Failed() {
			if execution, err := eng.Get(id); err == nil {
				for _, run := range execution.Runs {
					t.Logf("%s: %s (%s)", run.NodeID, run.Status, run.Error)
				}
			}
		}
	})
	waitSignoff := func(previous string) *NodeRun {
		t.Helper()
		var signoff *NodeRun
		waitFor(t, "editor sign-off", func() bool {
			for _, run := range eng.WaitingApprovals(id) {
				if run.NodeID == "editor_signoff" && run.ID != previous {
					signoff = run
					return true
				}
			}
			return false
		})
		return signoff
	}
	signoff := waitSignoff("")
	if !strings.Contains(signoff.Input, "Letter draft 2") {
		t.Fatalf("sign-off input = %q", signoff.Input)
	}
	if err := eng.Approve(id, signoff.ID, false, "editor", "Soften the market section"); err != nil {
		t.Fatal(err)
	}
	signoff = waitSignoff(signoff.ID)
	if err := eng.Approve(id, signoff.ID, true, "editor", "Send it"); err != nil {
		t.Fatal(err)
	}
	execution := waitDone(t, eng, id)
	if execution.Status != StatusCompleted {
		t.Fatalf("status = %s: %s", execution.Status, execution.Error)
	}
	profile := runByNode(execution, "manuscript_profile").Output
	for _, want := range []string{"Words: 577", "| 3 | Chapter Three |", "Worried the middle drags"} {
		if !strings.Contains(profile, want) {
			t.Errorf("profile missing %q:\n%s", want, profile)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	want := map[string]int{
		"structure_editor": 1, "character_editor": 1, "prose_editor": 1, "market_editor": 1,
		"continuity_editor": 2, // one schema repair
		"editorial_letter":  3, // gate rework, then human revision
		"letter_check":      3,
	}
	for nodeID, n := range want {
		if len(calls[nodeID]) != n {
			t.Errorf("%s calls = %d, want %d", nodeID, len(calls[nodeID]), n)
		}
	}
}
