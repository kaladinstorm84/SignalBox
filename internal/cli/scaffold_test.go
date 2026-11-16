package cli

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/kaladinstorm84/SignalBox/internal/engine"
	"github.com/kaladinstorm84/SignalBox/internal/knowledgebase"
)

func TestInitPreservesExistingFilesUnlessForced(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "project")
	if err := cmdInit([]string{dir}); err != nil {
		t.Fatal(err)
	}

	originals := map[string]string{}
	const userContent = "# My project-specific content\n"
	for _, name := range []string{
		"signalbox.yaml", "pipelines/default.yaml", ".gitignore", "AGENTS.md",
		"plugins/word-count/plugin.yaml", "plugins/word-count/main.py",
		"knowledge-bases/project-handbook/policy.md",
	} {
		path := filepath.Join(dir, filepath.FromSlash(name))
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read generated %s: %v", name, err)
		}
		originals[name] = string(content)
		if err := os.WriteFile(path, []byte(userContent), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if guide := originals["AGENTS.md"]; !strings.Contains(guide, "# SignalBox Project Guide") || !strings.Contains(guide, "signalbox validate") {
		t.Fatalf("generated AGENTS.md is missing project guidance:\n%s", guide)
	}
	for _, force := range []bool{false, true} {
		args := []string{dir}
		if force {
			args = append([]string{"--force"}, args...)
		}
		if err := cmdInit(args); err != nil {
			t.Fatal(err)
		}
		for name, original := range originals {
			content, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(name)))
			if err != nil {
				t.Fatal(err)
			}
			want := userContent
			if force {
				want = original
			}
			if string(content) != want {
				t.Fatalf("init (force=%v) wrote %s = %q, want %q", force, name, content, want)
			}
		}
	}
}

func TestInitDefaultPipelineUsesPluginAndKnowledge(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "project")
	if err := cmdInit([]string{dir}); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	cfg, pipelines, err := loadProject()
	if err != nil {
		t.Fatalf("load starter project: %v", err)
	}
	policy, err := os.ReadFile(filepath.Join(dir, "knowledge-bases", "project-handbook", "policy.md"))
	if err != nil {
		t.Fatal(err)
	}
	if len(policy) == 0 {
		t.Fatal("starter knowledge base is empty")
	}
	if _, err := exec.LookPath("python"); err != nil {
		t.Skip("Python is required to run the starter plugin")
	}
	eng, err := buildEngine(cfg, pipelines, false)
	if err != nil {
		t.Fatal(err)
	}
	id, err := eng.Start("default", "Explain reusable workflows")
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Cancel(id)
	done, err := eng.Wait(id)
	if err != nil {
		t.Fatal(err)
	}
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	timeout := time.NewTimer(15 * time.Second)
	defer timeout.Stop()
	approvals := 0
wait:
	for {
		select {
		case <-done:
			break wait
		case <-timeout.C:
			t.Fatal("starter pipeline did not finish within 15 seconds")
		case <-ticker.C:
			for _, run := range eng.WaitingApprovals(id) {
				if err := eng.Approve(id, run.ID, true, "test", ""); err != nil {
					t.Fatal(err)
				}
				approvals++
			}
		}
	}
	execution, err := eng.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	if execution.Status != engine.StatusCompleted || approvals != 1 {
		t.Fatalf("starter status = %s, approvals = %d", execution.Status, approvals)
	}
	var pluginOutput, writerOutput string
	for _, run := range execution.Runs {
		switch run.NodeID {
		case "word_counter":
			if run.Type != "plugin:word-count" || run.Status != engine.StatusCompleted {
				t.Fatalf("starter plugin run: %+v", run)
			}
			var result struct {
				Words int `json:"words"`
			}
			if err := json.Unmarshal([]byte(run.Output), &result); err != nil {
				t.Fatalf("word-count output: %v", err)
			}
			if want := len(strings.Fields(run.Input)); want == 0 || result.Words != want {
				t.Fatalf("word count = %d, want %d", result.Words, want)
			}
			pluginOutput = strings.TrimSpace(run.Output)
		case "writer":
			writerOutput = run.Output
		}
	}
	if pluginOutput == "" || !strings.Contains(writerOutput, pluginOutput) {
		t.Fatalf("writer did not receive the plugin result: %q", writerOutput)
	}
	if !strings.Contains(writerOutput, string(policy)) || !strings.Contains(writerOutput, "Explain reusable workflows") {
		t.Fatalf("writer did not receive the handbook and original request: %q", writerOutput)
	}
}

func TestInitProjectGuideExamples(t *testing.T) {
	dir := t.TempDir()
	if err := cmdInit([]string{dir}); err != nil {
		t.Fatal(err)
	}
	guide, err := os.ReadFile(filepath.Join(dir, "AGENTS.md"))
	if err != nil {
		t.Fatal(err)
	}
	examples := regexp.MustCompile(`(?s)~~~(?:yaml|python|markdown)\n# ([^\n]+)\n(.*?)\n~~~`).FindAllSubmatch(guide, -1)
	if len(examples) == 0 {
		t.Fatal("generated guide has no creation examples")
	}
	for _, example := range examples {
		path := filepath.Join(dir, filepath.FromSlash(string(example[1])))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, example[2], 0o644); err != nil {
			t.Fatal(err)
		}
	}

	t.Chdir(dir)
	_, pipelines, err := loadProject()
	if err != nil {
		t.Fatalf("load generated guide examples: %v", err)
	}
	if pipelines["count-words"] == nil {
		t.Fatal("guide is missing the plugin example pipeline")
	}
	handbook := pipelines["handbook-answer"]
	if handbook == nil {
		t.Fatal("guide is missing the knowledge-base example pipeline")
	}
	content, err := knowledgebase.New(dir).Content(handbook.Nodes[0].KnowledgeBases)
	if err != nil {
		t.Fatalf("load example knowledge: %v", err)
	}
	policy, err := os.ReadFile(filepath.Join(dir, "knowledge-bases", "project-handbook", "policy.md"))
	if err != nil {
		t.Fatal(err)
	}
	if len(policy) == 0 || !strings.Contains(content, string(policy)) {
		t.Fatalf("example agent does not receive its knowledge document: %q", content)
	}
}
