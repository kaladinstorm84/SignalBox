package runtime

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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

func TestOpenAIRuntimeCallOptions(t *testing.T) {
	t.Setenv("SB_TEST_KEY", "k")
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer srv.Close()
	for _, speed := range []string{"fast", "standard", ""} {
		rc := config.RuntimeConfig{Type: "openai", Model: "default-model", BaseURL: srv.URL, APIKeyEnv: "SB_TEST_KEY", Speed: speed}
		if speed != "" {
			rc.ReasoningEffort = "high"
		}
		rt, err := newOpenAI("ai", rc)
		if err != nil {
			t.Fatal(err)
		}
		for _, model := range []string{"", "override-model"} {
			body = nil
			if _, err := rt.Invoke(context.Background(), Request{Input: "hello", Model: model}); err != nil {
				t.Fatal(err)
			}
			if model == "" {
				model = "default-model"
			}
			if body["model"] != model {
				t.Fatalf("model = %v, want %s", body["model"], model)
			}
			if speed == "" {
				if _, ok := body["service_tier"]; ok {
					t.Fatal("service tier sent when unset")
				}
				if _, ok := body["reasoning_effort"]; ok {
					t.Fatal("reasoning effort sent when unset")
				}
			} else {
				tier := "priority"
				if speed == "standard" {
					tier = "default"
				}
				if body["service_tier"] != tier || body["reasoning_effort"] != "high" {
					t.Fatalf("call options = %v", body)
				}
			}
		}
	}
}

func TestOpenAIStreamsOutput(t *testing.T) {
	t.Setenv("SB_TEST_KEY", "k")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body["stream"] != true {
			t.Errorf("stream = %v, want true", body["stream"])
		}
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		for _, data := range []string{
			`{"choices":[{"delta":{"content":"Hello "}}]}`,
			`{"choices":[{"delta":{"content":"world"}}]}`,
			"[DONE]",
		} {
			_, _ = w.Write([]byte("data: " + data + "\n\n"))
			flusher.Flush()
		}
	}))
	defer srv.Close()

	rt, err := newOpenAI("o", config.RuntimeConfig{Model: "m", BaseURL: srv.URL, APIKeyEnv: "SB_TEST_KEY"})
	if err != nil {
		t.Fatal(err)
	}
	var chunks strings.Builder
	out, err := rt.(StreamingRuntime).InvokeStream(context.Background(), Request{Input: "x"}, func(chunk string) {
		chunks.WriteString(chunk)
	})
	if err != nil {
		t.Fatal(err)
	}
	if out != "Hello world" || chunks.String() != out {
		t.Fatalf("output = %q, streamed = %q", out, chunks.String())
	}
}

func TestOpenAIPerRequestTimeout(t *testing.T) {
	t.Setenv("SB_TEST_KEY", "k")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(1500 * time.Millisecond)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"late"}}]}`))
	}))
	defer srv.Close()
	rt, err := newOpenAI("o", config.RuntimeConfig{Model: "m", BaseURL: srv.URL, APIKeyEnv: "SB_TEST_KEY"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = rt.Invoke(context.Background(), Request{Input: "x", TimeoutSeconds: 1})
	if err == nil || !strings.Contains(err.Error(), "context deadline exceeded") {
		t.Fatalf("error = %v, want request timeout", err)
	}
}
