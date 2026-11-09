package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/kaladinstorm84/SignalBox/internal/engine"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestApprovalsResource(t *testing.T) {
	var mu sync.Mutex
	execution := engine.Execution{
		ID: "parent", Pipeline: "main", Status: engine.StatusRunning,
		Runs: []*engine.NodeRun{
			{ID: "review", NodeID: "human", Status: engine.StatusWaitingApproval, Input: "Review this proposal"},
			{ID: "resolved", Status: engine.StatusCompleted, ApprovedBy: "human"},
			{ID: "edit", Status: engine.StatusWaitingManualEdit},
			{ID: "gate", Status: engine.StatusRunning},
		},
		Surfaced: []engine.SurfacedRun{
			{Execution: "child", Pipeline: "nested", ParentRun: "child-node",
				Run: &engine.NodeRun{ID: "child-review", NodeID: "child-human", Status: engine.StatusWaitingApproval, Input: "Child proposal"}},
			{Execution: "child", Pipeline: "nested", ParentRun: "child-node",
				Run: &engine.NodeRun{ID: "child-edit", Status: engine.StatusWaitingManualEdit}},
			{Execution: "child", Pipeline: "nested", ParentRun: "child-node",
				Run: &engine.NodeRun{ID: "child-review", Status: engine.StatusWaitingApproval}},
			{Execution: "child"},
		},
	}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/pipelines":
			fmt.Fprint(w, "[]")
		case "/api/executions/parent":
			mu.Lock()
			defer mu.Unlock()
			if err := json.NewEncoder(w).Encode(execution); err != nil {
				t.Error(err)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer api.Close()
	s, err := New(context.Background(), Options{URL: api.URL, Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	updates := make(chan string, 10)
	session := connect(t, s, &mcp.ClientOptions{
		ResourceUpdatedHandler: func(_ context.Context, req *mcp.ResourceUpdatedNotificationRequest) {
			updates <- req.Params.URI
		},
	})
	templates, err := session.ListResourceTemplates(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, template := range templates.ResourceTemplates {
		found = found || template.URITemplate == "signalbox://executions/{id}/approvals{?offset,limit}"
	}
	if !found {
		t.Fatal("approvals resource template not discoverable")
	}
	const uri = "signalbox://executions/parent/approvals"
	type page struct {
		Approvals []pendingApproval `json:"approvals"`
		Total     int               `json:"total"`
		Next      *int              `json:"next_offset"`
	}
	readPage := func(target string) page {
		t.Helper()
		var result page
		if err := json.Unmarshal([]byte(read(t, session, target)), &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	full := readPage(uri)
	if full.Total != 2 || len(full.Approvals) != 2 || full.Next != nil {
		t.Fatalf("pending approvals = %#v", full)
	}
	if full.Approvals[0].Input != "Review this proposal" || full.Approvals[0].ExecutionID != "parent" {
		t.Fatalf("local approval = %#v", full.Approvals[0])
	}
	child := full.Approvals[1]
	if child.ExecutionID != "child" || child.RunID != "child-review" || child.ParentRunID != "child-node" ||
		child.Pipeline != "nested" || child.Input != "Child proposal" || child.ResourceURI != executionURI("child") {
		t.Fatalf("surfaced approval = %#v", child)
	}
	first := readPage(uri + "?limit=1")
	if len(first.Approvals) != 1 || first.Next == nil || *first.Next != 1 {
		t.Fatalf("first page = %#v", first)
	}
	if second := readPage(uri + "?offset=1&limit=1"); len(second.Approvals) != 1 || second.Approvals[0].RunID != child.RunID {
		t.Fatalf("second page = %#v", second)
	}
	for _, suffix := range []string{"?limit=101", "?offset=-1", "?offset=3", "?status=all"} {
		if _, err := session.ReadResource(context.Background(), &mcp.ReadResourceParams{URI: uri + suffix}); err == nil {
			t.Fatalf("invalid approvals query accepted: %s", suffix)
		}
	}
	if err := session.Subscribe(context.Background(), &mcp.SubscribeParams{URI: uri}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		s.mu.Lock()
		subscribed := false
		for _, resources := range s.subscriptions {
			_, subscribed = resources[uri]
			if subscribed {
				break
			}
		}
		s.mu.Unlock()
		if subscribed {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("subscription not registered")
		}
		time.Sleep(time.Millisecond)
	}
	mu.Lock()
	execution.Runs[0].Status = engine.StatusCompleted
	execution.Surfaced = nil
	mu.Unlock()
	if err := s.poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case updated := <-updates:
		if updated != uri {
			t.Fatalf("updated resource = %s", updated)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("approval resolution did not notify subscriber")
	}
	if empty := readPage(uri); empty.Total != 0 || empty.Approvals == nil || len(empty.Approvals) != 0 {
		t.Fatalf("resolved approvals = %#v", empty)
	}
	mu.Lock()
	execution.Status = engine.StatusCanceled
	execution.Runs[0].Status = engine.StatusWaitingApproval
	mu.Unlock()
	if terminal := readPage(uri); terminal.Total != 0 {
		t.Fatalf("terminal execution exposes actionable approvals: %#v", terminal)
	}
}
