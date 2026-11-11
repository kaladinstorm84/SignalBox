package engine

import (
	"path/filepath"
	"testing"

	"github.com/kaladinstorm84/SignalBox/internal/config"
	"github.com/kaladinstorm84/SignalBox/internal/pipeline"
	"github.com/kaladinstorm84/SignalBox/internal/runtime"
)

func TestSampleAIRetriesEscalateEffortAndReduceSpeed(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", "..", "testproject"))
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	pipelines, errs := pipeline.LoadDir(cfg.PipelinesPath(), pipeline.Options{Root: root, SchemasDir: cfg.SchemasPath()})
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	if err := runtime.ValidateProject(cfg, pipelines); err != nil {
		t.Fatal(err)
	}
	want := map[string]int{
		"context-strategies": 1, "discovery": 2, "knowledge-base-support": 1,
		"manuscript-review": 7, "Full Pipeline QA": 30, "review-kit": 3,
	}
	for name, expected := range want {
		p := pipelines[name]
		if p == nil {
			t.Fatalf("missing pipeline %q", name)
		}
		count := 0
		for _, node := range p.Nodes {
			if node.Retry == nil || node.Type == pipeline.TypePipeline {
				continue
			}
			count++
			attempts, backoff := 2, "2s"
			if name == "context-strategies" || name == "discovery" && node.ID == "quality_gate" ||
				name == "review-kit" && node.ID != "security_review" {
				attempts = 1
			}
			if name == "context-strategies" || name == "discovery" {
				backoff = "1s"
			}
			if node.Retry.Count != 0 || len(node.Retry.Attempts) != attempts || node.Retry.Backoff != backoff {
				t.Fatalf("%s/%s: expected %d fallback attempts and %s backoff: %+v", name, node.ID, attempts, backoff, node.Retry)
			}
			initial := []*pipeline.Node{&node}
			if node.Type == pipeline.TypeSession {
				initial = nil
				for _, id := range node.Participants {
					initial = append(initial, p.Node(id))
				}
			}
			for _, agent := range initial {
				if agent.Runtime != "codex" || agent.ReasoningEffort != "medium" || agent.Speed != "fast" {
					t.Errorf("%s/%s initial settings = %+v", name, agent.ID, agent)
				}
			}
			for i, attempt := range node.Retry.Attempts {
				effort := "high"
				if i == 1 {
					effort = "xhigh"
				}
				if attempt.Runtime != "" || attempt.ReasoningEffort != effort || attempt.Speed != "standard" {
					t.Errorf("%s/%s retry %d = %+v", name, node.ID, i, attempt)
				}
			}
		}
		if count != expected {
			t.Errorf("%s: %d retry-enabled nodes, want %d", name, count, expected)
		}
	}
}

// Scenario tests use mock handlers instead of a provider. Their call settings
// are validated separately above; mock has no reasoning or speed capabilities.
func clearSampleAIOptionsForMock(p *pipeline.Pipeline) {
	for i := range p.Nodes {
		node := &p.Nodes[i]
		node.ReasoningEffort, node.Speed = "", ""
		if node.Retry != nil {
			for j := range node.Retry.Attempts {
				node.Retry.Attempts[j].ReasoningEffort = ""
				node.Retry.Attempts[j].Speed = ""
			}
		}
	}
}
