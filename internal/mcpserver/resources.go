package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/kaladinstorm84/SignalBox/internal/engine"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func (s *Server) addResources() {
	for _, r := range []struct{ uri, name, description string }{
		{"signalbox://meta", "Project", "Project name and upload limits."},
		{"signalbox://pipelines", "Pipelines", "Available pipeline descriptions, parameters, input schema references and workflow nodes."},
		{"signalbox://schemas", "Schemas", "JSON Schema documents, usage references and load errors."},
		{"signalbox://executions", "Executions", "Recent execution summaries; defaults to page 1 with 20 entries."},
	} {
		s.server.AddResource(&mcp.Resource{
			URI: r.uri, Name: r.name, Description: r.description, MIMEType: "application/json",
		}, s.readResource)
	}
	for _, r := range []struct{ uri, name, description string }{
		{"signalbox://pipelines/{name}", "Pipeline", "One pipeline's API projection. Read its input_schema from the schema resources before starting."},
		{"signalbox://schemas/{name}", "Schema", "One JSON Schema document with usage references."},
		{"signalbox://executions{?page,page_size}", "Execution page", "API pagination: page >= 1; page_size is a multiple of 10 between 10 and 100."},
		{"signalbox://executions/{id}", "Execution", "Full API execution detail, including runs, pending actions and surfaced child runs. Maximum 1 MiB."},
		{"signalbox://executions/{id}/results{?offset,limit}", "Results", "Final outputs, status and output-resource links. Zero-based offset; limit defaults to 20, maximum 100."},
		{"signalbox://executions/{id}/approvals{?offset,limit}", "Pending approvals", "Pending human approval gates, including surfaced child gates, with review input and owning execution/run IDs. Read-only and subscribable without --allow-review. Zero-based offset; limit defaults to 20, maximum 100."},
		{"signalbox://executions/{id}/chunks", "Chunks", "Current execution work items."},
		{"signalbox://executions/{id}/runs/{run_id}/output{?offset,limit}", "Run output", "Output text in Unicode-character pages. Zero-based offset; limit defaults to 8192, maximum 65536. Includes total and next_offset."},
		{"signalbox://executions/{id}/runs/{run_id}/transcript{?offset,limit}", "Session transcript", "Session outcome and discussion turns. Zero-based turn offset; limit defaults to 20, maximum 100. Maximum 1 MiB; reduce limit for large turns."},
		{"signalbox://executions/{id}/files/{name}", "Attachment", "Attached file contents as a binary resource, maximum 1 MiB. Larger files remain available through the HTTP API."},
	} {
		s.server.AddResourceTemplate(&mcp.ResourceTemplate{
			URITemplate: r.uri, Name: r.name, Description: r.description,
		}, s.readResource)
	}
}

func (s *Server) readResource(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
	return s.readURI(ctx, req.Params.URI)
}

// readURI accepts only the adapter's resource namespace and validates decoded
// path segments before mapping them to API requests.
func (s *Server) readURI(ctx context.Context, uri string) (*mcp.ReadResourceResult, error) {
	u, err := url.Parse(uri)
	if err != nil || u.Scheme != "signalbox" || u.User != nil || u.Fragment != "" {
		return nil, mcp.ResourceNotFoundError(uri)
	}
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return nil, fmt.Errorf("invalid resource query: %w", err)
	}
	parts := []string{}
	if u.EscapedPath() != "" {
		for _, raw := range strings.Split(strings.TrimPrefix(u.EscapedPath(), "/"), "/") {
			value, err := url.PathUnescape(raw)
			if err != nil {
				return nil, mcp.ResourceNotFoundError(uri)
			}
			if _, err := segment(value); err != nil {
				return nil, err
			}
			parts = append(parts, value)
		}
	}
	data, mimeType, err := s.resourceData(ctx, u.Host, parts, query)
	if err != nil {
		var apiErr *apiError
		if errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound {
			return nil, mcp.ResourceNotFoundError(uri)
		}
		return nil, err
	}
	if len(data) > maxResourceBytes {
		return nil, fmt.Errorf("resource exceeds %d bytes; use a smaller page, a run output resource, or the HTTP API/dashboard", maxResourceBytes)
	}
	content := &mcp.ResourceContents{URI: uri, MIMEType: mimeType}
	if u.Host == "executions" && len(parts) == 3 && parts[1] == "files" {
		content.Blob = data
	} else {
		content.Text = string(data)
	}
	return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{content}}, nil
}

func (s *Server) resourceData(ctx context.Context, host string, parts []string, query url.Values) ([]byte, string, error) {
	allowed := []string{}
	if host == "executions" && len(parts) == 0 {
		allowed = []string{"page", "page_size"}
	} else if host == "executions" && (len(parts) == 2 && (parts[1] == "results" || parts[1] == "approvals") ||
		len(parts) == 4 && parts[1] == "runs" && (parts[3] == "output" || parts[3] == "transcript")) {
		allowed = []string{"offset", "limit"}
	}
	for name, values := range query {
		found := false
		for _, valid := range allowed {
			found = found || name == valid
		}
		if !found || len(values) != 1 {
			return nil, "", fmt.Errorf("unsupported or repeated resource query parameter %q", name)
		}
	}
	if len(parts) == 0 {
		switch host {
		case "meta", "pipelines", "schemas":
			return s.request(ctx, http.MethodGet, "/"+host, nil)
		case "executions":
			page, err := intQuery(query, "page", 1, 1, 1_000_000)
			if err != nil {
				return nil, "", err
			}
			size, err := intQuery(query, "page_size", 20, 10, 100)
			if err != nil {
				return nil, "", err
			}
			if size%10 != 0 {
				return nil, "", fmt.Errorf("page_size must be a multiple of 10")
			}
			return s.request(ctx, http.MethodGet, fmt.Sprintf("/executions?page=%d&page_size=%d", page, size), nil)
		}
	}
	if len(parts) == 1 && (host == "pipelines" || host == "schemas") {
		data, _, err := s.request(ctx, http.MethodGet, "/"+host, nil)
		if err != nil {
			return nil, "", err
		}
		var items []json.RawMessage
		if host == "schemas" {
			var catalog struct {
				Schemas []json.RawMessage `json:"schemas"`
				Errors  []string          `json:"errors"`
			}
			if err := json.Unmarshal(data, &catalog); err != nil {
				return nil, "", fmt.Errorf("decoding schemas: %w", err)
			}
			if len(catalog.Errors) > 0 {
				return nil, "", fmt.Errorf("schema loading errors: %s", strings.Join(catalog.Errors, "; "))
			}
			items = catalog.Schemas
		} else if err := json.Unmarshal(data, &items); err != nil {
			return nil, "", fmt.Errorf("decoding pipelines: %w", err)
		}
		for _, item := range items {
			var named struct {
				Name string `json:"name"`
			}
			if err := json.Unmarshal(item, &named); err != nil {
				return nil, "", err
			}
			if named.Name == parts[0] {
				return item, "application/json", nil
			}
		}
		return nil, "", mcp.ResourceNotFoundError("signalbox://" + host + "/" + url.PathEscape(parts[0]))
	}
	if host != "executions" || len(parts) == 0 {
		return nil, "", fmt.Errorf("unsupported SignalBox resource")
	}
	path := "/executions/" + url.PathEscape(parts[0])
	if len(parts) == 1 {
		return s.request(ctx, http.MethodGet, path, nil)
	}
	if len(parts) == 2 && parts[1] == "chunks" {
		return s.request(ctx, http.MethodGet, path+"/chunks", nil)
	}
	if len(parts) == 3 && parts[1] == "files" {
		return s.request(ctx, http.MethodGet, path+"/files/"+url.PathEscape(parts[2]), nil)
	}
	isResults := len(parts) == 2 && parts[1] == "results"
	isApprovals := len(parts) == 2 && parts[1] == "approvals"
	isRun := len(parts) == 4 && parts[1] == "runs" && (parts[3] == "output" || parts[3] == "transcript")
	if !isResults && !isApprovals && !isRun {
		return nil, "", fmt.Errorf("unsupported execution resource")
	}
	data, _, err := s.request(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, "", err
	}
	var execution engine.Execution
	if err := json.Unmarshal(data, &execution); err != nil {
		return nil, "", fmt.Errorf("decoding execution: %w", err)
	}
	if isApprovals {
		return approvalData(&execution, query)
	}
	if isResults {
		start, end, err := pageRange(query, len(execution.FinalOutputs), 20, 100)
		if err != nil {
			return nil, "", err
		}
		outputs := make([]map[string]any, 0, end-start)
		for _, output := range execution.FinalOutputs[start:end] {
			outputs = append(outputs, map[string]any{
				"node_id": output.NodeID, "run_id": output.NodeRunID, "output": output.Output,
				"resource_uri": executionURI(execution.ID) + "/runs/" + url.PathEscape(output.NodeRunID) + "/output",
			})
		}
		return jsonData(map[string]any{
			"execution_id": execution.ID, "status": execution.Status, "terminal": execution.Status.Terminal(),
			"error": execution.Error, "outputs": outputs, "offset": start,
			"total": len(execution.FinalOutputs), "next_offset": nextOffset(end, len(execution.FinalOutputs)),
		})
	}
	for _, run := range execution.Runs {
		if run == nil || run.ID != parts[2] {
			continue
		}
		if parts[3] == "output" {
			text := []rune(run.Output)
			start, end, err := pageRange(query, len(text), 8192, 65536)
			if err != nil {
				return nil, "", err
			}
			return jsonData(map[string]any{
				"run_id": run.ID, "status": run.Status, "output": string(text[start:end]),
				"offset": start, "total": len(text), "next_offset": nextOffset(end, len(text)),
			})
		}
		if run.Session == nil {
			return nil, "", fmt.Errorf("run %q is not a session", run.ID)
		}
		start, end, err := pageRange(query, len(run.Session.Turns), 20, 100)
		if err != nil {
			return nil, "", err
		}
		return jsonData(map[string]any{
			"run_id": run.ID, "status": run.Status, "round": run.Session.Round,
			"max_rounds": run.Session.MaxRounds, "outcome": run.Session.Outcome,
			"consensus_round": run.Session.ConsensusRound, "turns": run.Session.Turns[start:end],
			"offset": start, "total": len(run.Session.Turns), "next_offset": nextOffset(end, len(run.Session.Turns)),
		})
	}
	return nil, "", fmt.Errorf("run %q was not found in execution %q", parts[2], execution.ID)
}

type pendingApproval struct {
	ExecutionID string        `json:"execution_id"`
	Pipeline    string        `json:"pipeline"`
	RunID       string        `json:"run_id"`
	NodeID      string        `json:"node_id"`
	Status      engine.Status `json:"status"`
	Input       string        `json:"input"`
	ResourceURI string        `json:"resource_uri"`
	ParentRunID string        `json:"parent_run_id,omitempty"`
}

func approvalData(execution *engine.Execution, query url.Values) ([]byte, string, error) {
	approvals := []pendingApproval{}
	seen := map[[2]string]bool{}
	add := func(id, pipeline, parentRun string, run *engine.NodeRun) {
		if run == nil || run.Status != engine.StatusWaitingApproval || execution.Status.Terminal() {
			return
		}
		key := [2]string{id, run.ID}
		if seen[key] {
			return
		}
		seen[key] = true
		approvals = append(approvals, pendingApproval{
			ExecutionID: id, Pipeline: pipeline, RunID: run.ID, NodeID: run.NodeID,
			Status: run.Status, Input: run.Input, ResourceURI: executionURI(id), ParentRunID: parentRun,
		})
	}
	for _, run := range execution.Runs {
		add(execution.ID, execution.Pipeline, "", run)
	}
	for _, surfaced := range execution.Surfaced {
		add(surfaced.Execution, surfaced.Pipeline, surfaced.ParentRun, surfaced.Run)
	}
	start, end, err := pageRange(query, len(approvals), 20, 100)
	if err != nil {
		return nil, "", err
	}
	return jsonData(map[string]any{
		"execution_id": execution.ID, "status": execution.Status, "approvals": approvals[start:end],
		"offset": start, "total": len(approvals), "next_offset": nextOffset(end, len(approvals)),
	})
}

func intQuery(query url.Values, name string, fallback, min, max int) (int, error) {
	values, ok := query[name]
	if !ok {
		return fallback, nil
	}
	if len(values) != 1 {
		return 0, fmt.Errorf("%s must occur once", name)
	}
	n, err := strconv.Atoi(values[0])
	if err != nil || n < min || n > max {
		return 0, fmt.Errorf("%s must be an integer between %d and %d", name, min, max)
	}
	return n, nil
}

func pageRange(query url.Values, total, defaultLimit, maxLimit int) (int, int, error) {
	start, err := intQuery(query, "offset", 0, 0, total)
	if err != nil {
		return 0, 0, err
	}
	limit, err := intQuery(query, "limit", defaultLimit, 1, maxLimit)
	if err != nil {
		return 0, 0, err
	}
	return start, min(start+limit, total), nil
}

func nextOffset(end, total int) *int {
	if end < total {
		return &end
	}
	return nil
}

func jsonData(value any) ([]byte, string, error) {
	data, err := json.Marshal(value)
	return data, "application/json", err
}
