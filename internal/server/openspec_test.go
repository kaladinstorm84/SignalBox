package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kaladinstorm84/SignalBox/internal/config"
	"github.com/kaladinstorm84/SignalBox/internal/engine"
	"github.com/kaladinstorm84/SignalBox/internal/pipeline"
	"github.com/kaladinstorm84/SignalBox/internal/runtime"
)

func TestOpenSpecPipelineView(t *testing.T) {
	p, err := pipeline.Parse([]byte(`
name: spec
agents:
  - id: status
    type: openspec
    timeout_seconds: 30
    openspec:
      command: custom-openspec
      args: [status, --change, add-export, --json]
`))
	if err != nil {
		t.Fatal(err)
	}
	runtimes, err := runtime.NewRegistry(map[string]config.RuntimeConfig{})
	if err != nil {
		t.Fatal(err)
	}
	pipelines := map[string]*pipeline.Pipeline{p.Name: p}
	eng, err := engine.New(pipelines, runtimes, nil)
	if err != nil {
		t.Fatal(err)
	}
	handler := New(eng, "test", func() (map[string]*pipeline.Pipeline, error) { return pipelines, nil }, nil, nil).Handler()
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/pipelines", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", response.Code, response.Body.String())
	}
	var views []pipelineView
	if err := json.Unmarshal(response.Body.Bytes(), &views); err != nil {
		t.Fatal(err)
	}
	if len(views) != 1 || len(views[0].Nodes) != 1 {
		t.Fatalf("pipelines = %s", response.Body.String())
	}
	node := views[0].Nodes[0]
	if node.Type != "openspec" || node.OpenSpec == nil || node.OpenSpec.Command != "custom-openspec" ||
		len(node.OpenSpec.Args) != 4 || node.OpenSpec.Args[2] != "add-export" || node.TimeoutSeconds != 30 || node.Runtime != "" {
		t.Fatalf("node = %+v", node)
	}
}
