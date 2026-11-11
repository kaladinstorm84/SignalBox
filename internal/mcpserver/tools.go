package mcpserver

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/url"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type fileInput struct {
	Name          string `json:"name" jsonschema:"Attachment filename, not a local filesystem path"`
	ContentBase64 string `json:"content_base64" jsonschema:"Base64 encoded file contents"`
}

type startInput struct {
	Pipeline string            `json:"pipeline" jsonschema:"Pipeline name discovered from signalbox://pipelines"`
	Input    string            `json:"input" jsonschema:"Pipeline input text; JSON encoded text when the pipeline declares an input schema"`
	Params   map[string]string `json:"params,omitempty" jsonschema:"Pipeline parameters using their API string representation"`
	Files    []fileInput       `json:"files,omitempty" jsonschema:"Optional attachments; combined decoded contents must not exceed 8 MiB and API upload limits also apply"`
}

type executionInput struct {
	ExecutionID string `json:"execution_id"`
}

type retryInput struct {
	ExecutionID string `json:"execution_id"`
	RunID       string `json:"run_id" jsonschema:"Failed run awaiting retry, including a surfaced child run"`
}

type restartInput struct {
	ExecutionID string `json:"execution_id"`
	RunID       string `json:"run_id,omitempty"`
	NodeID      string `json:"node_id,omitempty"`
	Stage       string `json:"stage,omitempty"`
	Force       bool   `json:"force,omitempty" jsonschema:"Explicitly accept a changed pipeline definition; do not set automatically after a conflict"`
}

type approvalInput struct {
	ExecutionID string `json:"execution_id"`
	RunID       string `json:"run_id"`
	Approved    bool   `json:"approved"`
	Comment     string `json:"comment,omitempty"`
}

type editInput struct {
	ExecutionID string `json:"execution_id"`
	RunID       string `json:"run_id"`
	Output      string `json:"output"`
}

type addChunkInput struct {
	ExecutionID string `json:"execution_id"`
	Title       string `json:"title"`
}

type chunkInput struct {
	ExecutionID string `json:"execution_id"`
	ChunkID     string `json:"chunk_id"`
}

type commandOutput struct {
	ExecutionID string         `json:"execution_id"`
	ResourceURI string         `json:"resource_uri"`
	Dashboard   string         `json:"dashboard_url"`
	Response    map[string]any `json:"response" jsonschema:"Original API response; an execution ID acknowledges creation, not completion"`
}

func tool(name, description string, destructive bool) *mcp.Tool {
	openWorld := true
	return &mcp.Tool{
		Name: name, Description: description,
		Annotations: &mcp.ToolAnnotations{DestructiveHint: &destructive, OpenWorldHint: &openWorld},
	}
}

// addTools omits review mutations entirely unless explicitly enabled;
// descriptive tool annotations alone do not enforce that boundary.
func (s *Server) addTools(allowReview bool) {
	mcp.AddTool(s.server, tool("start_execution", "Start a pipeline asynchronously. Read its pipeline resource first. Returns an execution handle, not completed results. Repeated calls create separate executions; do not automatically retry an ambiguous timeout.", true),
		func(ctx context.Context, _ *mcp.CallToolRequest, in startInput) (*mcp.CallToolResult, commandOutput, error) {
			if in.Pipeline == "" {
				return nil, commandOutput{}, fmt.Errorf("pipeline is required")
			}
			var data []byte
			var err error
			if len(in.Files) == 0 {
				data, _, err = s.request(ctx, http.MethodPost, "/executions", map[string]any{
					"pipeline": in.Pipeline, "input": in.Input, "params": in.Params,
				})
			} else {
				data, err = s.startWithFiles(ctx, in)
			}
			return s.commandResult("", data, err, true)
		})
	mcp.AddTool(s.server, tool("cancel_execution", "Cancel a running execution, using the same semantics as the HTTP API.", true),
		func(ctx context.Context, _ *mcp.CallToolRequest, in executionInput) (*mcp.CallToolResult, commandOutput, error) {
			return s.command(ctx, in.ExecutionID, "cancel", nil, false)
		})
	mcp.AddTool(s.server, tool("rerun_execution", "Create a new execution using the original input, parameters and files and the current pipeline definition. Child executions cannot be rerun directly.", true),
		func(ctx context.Context, _ *mcp.CallToolRequest, in executionInput) (*mcp.CallToolResult, commandOutput, error) {
			return s.command(ctx, in.ExecutionID, "rerun", nil, true)
		})
	mcp.AddTool(s.server, tool("retry_run", "Retry a failed agent call awaiting retry; accepts surfaced child run IDs.", true),
		func(ctx context.Context, _ *mcp.CallToolRequest, in retryInput) (*mcp.CallToolResult, commandOutput, error) {
			return s.command(ctx, in.ExecutionID, "retry", map[string]string{"run_id": in.RunID}, false)
		})
	mcp.AddTool(s.server, tool("restart_execution", "Create a new execution from a finished execution's recovery point. Definition changes return a conflict unless force is explicitly authorized.", true),
		func(ctx context.Context, _ *mcp.CallToolRequest, in restartInput) (*mcp.CallToolResult, commandOutput, error) {
			return s.command(ctx, in.ExecutionID, "restart", map[string]any{
				"run_id": in.RunID, "node_id": in.NodeID, "stage": in.Stage, "force": in.Force,
			}, true)
		})
	mcp.AddTool(s.server, tool("add_chunk", "Add an execution work item. Actor is recorded as mcp-client.", false),
		func(ctx context.Context, _ *mcp.CallToolRequest, in addChunkInput) (*mcp.CallToolResult, commandOutput, error) {
			return s.command(ctx, in.ExecutionID, "chunks", map[string]string{"title": in.Title, "by": "mcp-client"}, false)
		})
	mcp.AddTool(s.server, tool("complete_chunk", "Mark an execution work item done. Actor is recorded as mcp-client.", true),
		func(ctx context.Context, _ *mcp.CallToolRequest, in chunkInput) (*mcp.CallToolResult, commandOutput, error) {
			chunk, err := segment(in.ChunkID)
			if err != nil {
				return nil, commandOutput{}, err
			}
			return s.command(ctx, in.ExecutionID, "chunks/"+chunk+"/done", map[string]string{"by": "mcp-client"}, false)
		})
	mcp.AddTool(s.server, tool("remove_chunk", "Remove an execution work item.", true),
		func(ctx context.Context, _ *mcp.CallToolRequest, in chunkInput) (*mcp.CallToolResult, commandOutput, error) {
			id, err := segment(in.ExecutionID)
			if err != nil {
				return nil, commandOutput{}, err
			}
			chunk, err := segment(in.ChunkID)
			if err != nil {
				return nil, commandOutput{}, err
			}
			data, _, err := s.request(ctx, http.MethodDelete, "/executions/"+id+"/chunks/"+chunk, nil)
			return s.commandResult(in.ExecutionID, data, err, false)
		})
	if allowReview {
		mcp.AddTool(s.server, tool("resolve_approval", "Resolve an approval gate under explicitly delegated review policy. Recorded as mcp-client, never as a human reviewer.", true),
			func(ctx context.Context, _ *mcp.CallToolRequest, in approvalInput) (*mcp.CallToolResult, commandOutput, error) {
				return s.command(ctx, in.ExecutionID, "approve", map[string]any{
					"run_id": in.RunID, "approved": in.Approved, "comment": in.Comment, "approved_by": "mcp-client",
				}, false)
			})
		mcp.AddTool(s.server, tool("submit_manual_edit", "Submit replacement text to a manual-edit gate under explicitly delegated review policy. Recorded as mcp-client.", true),
			func(ctx context.Context, _ *mcp.CallToolRequest, in editInput) (*mcp.CallToolResult, commandOutput, error) {
				return s.command(ctx, in.ExecutionID, "manual-edit", map[string]string{
					"run_id": in.RunID, "output": in.Output, "edited_by": "mcp-client",
				}, false)
			})
	}
}

func (s *Server) command(ctx context.Context, execution, action string, body any, creates bool) (*mcp.CallToolResult, commandOutput, error) {
	id, err := segment(execution)
	if err != nil {
		return nil, commandOutput{}, err
	}
	data, _, err := s.request(ctx, http.MethodPost, "/executions/"+id+"/"+action, body)
	return s.commandResult(execution, data, err, creates)
}

func (s *Server) commandResult(id string, data []byte, err error, creates bool) (*mcp.CallToolResult, commandOutput, error) {
	if err != nil {
		return nil, commandOutput{}, err
	}
	var response map[string]any
	if err := json.Unmarshal(data, &response); err != nil || response == nil {
		return nil, commandOutput{}, fmt.Errorf("SignalBox API returned an invalid JSON object")
	}
	if creates {
		var response struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(data, &response); err != nil {
			return nil, commandOutput{}, fmt.Errorf("decoding created execution: %w", err)
		}
		if _, err := segment(response.ID); err != nil {
			return nil, commandOutput{}, fmt.Errorf("API returned an invalid execution ID: %w", err)
		}
		id = response.ID
	}
	uri := executionURI(id)
	out := commandOutput{
		ExecutionID: id, ResourceURI: uri,
		Dashboard: s.base + "/#/exec/" + url.PathEscape(id), Response: response,
	}
	text, err := json.Marshal(out)
	if err != nil {
		return nil, commandOutput{}, err
	}
	return &mcp.CallToolResult{Content: []mcp.Content{
		&mcp.TextContent{Text: string(text)},
		&mcp.ResourceLink{URI: uri, Name: "Execution " + id, MIMEType: "application/json"},
	}}, out, nil
}

func (s *Server) startWithFiles(ctx context.Context, in startInput) ([]byte, error) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	params, err := json.Marshal(in.Params)
	if err != nil {
		return nil, err
	}
	for _, field := range []struct{ name, value string }{
		{"pipeline", in.Pipeline}, {"input", in.Input}, {"params", string(params)},
	} {
		if err := writer.WriteField(field.name, field.value); err != nil {
			return nil, err
		}
	}
	total := 0
	for _, file := range in.Files {
		if _, err := segment(file.Name); err != nil {
			return nil, err
		}
		if len(file.ContentBase64) > base64.StdEncoding.EncodedLen(8<<20) {
			return nil, fmt.Errorf("MCP attachments exceed 8 MiB")
		}
		data, err := base64.StdEncoding.DecodeString(file.ContentBase64)
		if err != nil {
			return nil, fmt.Errorf("decoding attachment %q: %w", file.Name, err)
		}
		total += len(data)
		if total > 8<<20 {
			return nil, fmt.Errorf("MCP attachments exceed 8 MiB")
		}
		part, err := writer.CreateFormFile("file", file.Name)
		if err != nil {
			return nil, err
		}
		if _, err := part.Write(data); err != nil {
			return nil, err
		}
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	data, _, err := s.requestBody(ctx, http.MethodPost, "/executions", &body, writer.FormDataContentType())
	return data, err
}
