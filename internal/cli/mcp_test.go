package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestMCPStdioProcess(t *testing.T) {
	if os.Getenv("SIGNALBOX_TEST_MCP_PROCESS") != "1" {
		return
	}
	os.Exit(Main([]string{"mcp", "--url", os.Getenv("SIGNALBOX_TEST_MCP_URL")}))
}

func TestMCPCommandOverStdio(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/pipelines":
			fmt.Fprint(w, `[{"name":"demo","description":"Example pipeline"}]`)
		case "/api/executions":
			fmt.Fprint(w, `{"id":"stdio-execution"}`)
		case "/api/executions/stdio-execution":
			fmt.Fprint(w, `{"id":"stdio-execution","status":"completed"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer api.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestMCPStdioProcess$")
	cmd.Env = append(os.Environ(), "SIGNALBOX_TEST_MCP_PROCESS=1", "SIGNALBOX_TEST_MCP_URL="+api.URL)
	client := mcp.NewClient(&mcp.Implementation{Name: "stdio-test", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.CommandTransport{Command: cmd}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	tools, err := session.ListTools(ctx, nil)
	if err != nil || len(tools.Tools) != 8 {
		t.Fatalf("stdio tools = %#v, %v", tools, err)
	}
	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "start_execution", Arguments: map[string]any{"pipeline": "demo", "input": "hello"},
	})
	if err != nil || result.IsError || result.StructuredContent == nil {
		t.Fatalf("stdio start = %#v, %v", result, err)
	}
	resource, err := session.ReadResource(ctx, &mcp.ReadResourceParams{URI: "signalbox://executions/stdio-execution"})
	if err != nil || len(resource.Contents) != 1 {
		t.Fatalf("stdio resource = %#v, %v", resource, err)
	}
}

func TestMCPCommandArguments(t *testing.T) {
	for _, args := range [][]string{
		{"--unknown"}, {"unexpected"}, {"--url", "file:///invalid"},
	} {
		if err := cmdMCP(args); err == nil {
			t.Fatalf("accepted invalid arguments: %v", args)
		}
	}
	if err := cmdMCP([]string{"--help"}); err != nil {
		t.Fatal(err)
	}
}

func TestMCPApprovalSubscriptionsOverStdio(t *testing.T) {
	const uri = "signalbox://executions/review-execution/approvals"
	var pending atomic.Bool
	baselineRead := make(chan struct{}, 10)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/pipelines":
			fmt.Fprint(w, "[]")
		case "/api/executions/review-execution":
			if pending.Load() {
				fmt.Fprint(w, `{"id":"review-execution","pipeline":"demo","status":"running","runs":[{"id":"review","node_id":"human","status":"waiting_approval","input":"Review this proposal"}]}`)
			} else {
				fmt.Fprint(w, `{"id":"review-execution","pipeline":"demo","status":"running","runs":[]}`)
			}
			select {
			case baselineRead <- struct{}{}:
			default:
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer api.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestMCPStdioProcess$")
	cmd.Env = append(os.Environ(), "SIGNALBOX_TEST_MCP_PROCESS=1", "SIGNALBOX_TEST_MCP_URL="+api.URL)
	updates := make(chan string, 10)
	client := mcp.NewClient(&mcp.Implementation{Name: "subscription-test", Version: "1"}, &mcp.ClientOptions{
		ResourceUpdatedHandler: func(_ context.Context, req *mcp.ResourceUpdatedNotificationRequest) {
			select {
			case updates <- req.Params.URI:
			case <-ctx.Done():
			}
		},
	})
	session, err := client.Connect(ctx, &mcp.CommandTransport{Command: cmd}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if err := session.Subscribe(ctx, &mcp.SubscribeParams{URI: uri}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-baselineRead:
	case <-ctx.Done():
		t.Fatal("subscription did not establish its baseline")
	}
	for _, wantPending := range []bool{true, false} {
		pending.Store(wantPending)
		select {
		case got := <-updates:
			if got != uri {
				t.Fatalf("updated resource = %q, want %q", got, uri)
			}
		case <-ctx.Done():
			t.Fatalf("automatic notification missing with pending approval = %v", wantPending)
		}
		resource, err := session.ReadResource(ctx, &mcp.ReadResourceParams{URI: uri})
		if err != nil {
			t.Fatal(err)
		}
		if len(resource.Contents) != 1 {
			t.Fatalf("resource contents = %#v", resource.Contents)
		}
		var page struct {
			Approvals []struct {
				RunID string `json:"run_id"`
				Input string `json:"input"`
			} `json:"approvals"`
			Total int `json:"total"`
		}
		if err := json.Unmarshal([]byte(resource.Contents[0].Text), &page); err != nil {
			t.Fatal(err)
		}
		if wantPending {
			if page.Total != 1 || len(page.Approvals) != 1 ||
				page.Approvals[0].RunID != "review" || page.Approvals[0].Input != "Review this proposal" {
				t.Fatalf("new approval = %#v", page)
			}
		} else if page.Total != 0 || len(page.Approvals) != 0 {
			t.Fatalf("resolved approval still pending: %#v", page)
		}
	}
	if err := session.Unsubscribe(ctx, &mcp.UnsubscribeParams{URI: uri}); err != nil {
		t.Fatal(err)
	}
}
