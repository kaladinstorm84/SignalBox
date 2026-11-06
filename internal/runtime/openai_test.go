package runtime

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kaladinstorm84/SignalBox/internal/config"
	"github.com/kaladinstorm84/SignalBox/internal/schema"
)

func TestOpenAIRequestsStructuredOutput(t *testing.T) {
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		bodies = append(bodies, body)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":" {\"ok\": true} "}}]}`))
	}))
	defer srv.Close()
	t.Setenv("SB_TEST_KEY", "k")
	rt, err := newOpenAI("o", config.RuntimeConfig{Model: "m", BaseURL: srv.URL, APIKeyEnv: "SB_TEST_KEY"})
	if err != nil {
		t.Fatal(err)
	}
	sch, err := schema.Compile("release.note v2", []byte(`{"type":"object","properties":{"ok":{"type":"boolean"}}}`), false)
	if err != nil {
		t.Fatal(err)
	}
	if out, err := rt.Invoke(context.Background(), Request{Input: "x", Schema: sch}); err != nil || out != `{"ok": true}` {
		t.Fatalf("invoke = %q %v", out, err)
	}
	if _, err := rt.Invoke(context.Background(), Request{Input: "x"}); err != nil {
		t.Fatal(err)
	}
	format, ok := bodies[0]["response_format"].(map[string]any)
	if !ok || format["type"] != "json_schema" {
		t.Fatalf("response_format = %v", bodies[0]["response_format"])
	}
	spec := format["json_schema"].(map[string]any)
	if spec["name"] != "release_note_v2" || spec["schema"].(map[string]any)["type"] != "object" {
		t.Fatalf("json_schema = %v", spec)
	}
	if _, present := bodies[1]["response_format"]; present {
		t.Fatal("response_format sent without a schema")
	}
}
