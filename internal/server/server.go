// Package server exposes the web UI and JSON API for tracking executions and
// starting new pipeline runs.
package server

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"mime"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kaladinstorm84/SignalBox/internal/config"
	"github.com/kaladinstorm84/SignalBox/internal/engine"
	"github.com/kaladinstorm84/SignalBox/internal/pipeline"
	"github.com/kaladinstorm84/SignalBox/internal/plugin"
	"github.com/kaladinstorm84/SignalBox/internal/watcher"
	"github.com/yuin/goldmark"
)

var markdown = goldmark.New()

//go:embed ui
var uiFS embed.FS

// Server wires the engine to HTTP handlers.
type Server struct {
	eng           *engine.Engine
	project       string
	loadPipelines func() (map[string]*pipeline.Pipeline, error)
	watcher       *watcher.Runner
	watchers      []config.WatcherConfig
	projectRoot   string
	settingsMu    sync.Mutex
}

// New creates a server for the given engine, pipeline loader, and watchers.
func New(eng *engine.Engine, project string, loadPipelines func() (map[string]*pipeline.Pipeline, error), watcherRunner *watcher.Runner, watchers []config.WatcherConfig, projectRoot ...string) *Server {
	s := &Server{eng: eng, project: project, loadPipelines: loadPipelines, watcher: watcherRunner, watchers: watchers}
	if len(projectRoot) > 0 {
		s.projectRoot = projectRoot[0]
	}
	return s
}

// refreshPipelines replaces definitions only after the loader accepts the
// project. Existing executions keep the definitions they started with.
func (s *Server) refreshPipelines() error {
	pipelines, err := s.loadPipelines()
	if err != nil {
		return err
	}
	s.eng.SetPipelines(pipelines)
	return nil
}

// Handler builds the HTTP routing table.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	ui, err := fs.Sub(uiFS, "ui")
	if err != nil {
		panic(err)
	}
	mux.Handle("GET /", http.FileServerFS(ui))

	mux.HandleFunc("GET /api/meta", s.handleMeta)
	mux.HandleFunc("GET /api/dashboard", s.handleDashboard)
	mux.HandleFunc("GET /api/settings", s.handleGetSettings)
	mux.HandleFunc("PUT /api/settings", s.handleSaveSettings)
	mux.HandleFunc("GET /api/watchers", s.handleGetWatchers)
	mux.HandleFunc("PUT /api/watchers", s.handleSaveWatchers)
	mux.HandleFunc("GET /api/pipelines", s.handlePipelines)
	mux.HandleFunc("GET /api/schemas", s.handleSchemas)
	mux.HandleFunc("GET /api/pipeline-files", s.handleListPipelineFiles)
	mux.HandleFunc("GET /api/pipeline-files/{filename}", s.handleGetPipelineFile)
	mux.HandleFunc("PUT /api/pipeline-files/{filename}", s.handleSavePipelineFile)
	mux.HandleFunc("GET /api/knowledge-bases", s.handleListKnowledgeBases)
	mux.HandleFunc("POST /api/knowledge-bases", s.handleCreateKnowledgeBase)
	mux.HandleFunc("DELETE /api/knowledge-bases/{name}", s.handleDeleteKnowledgeBase)
	mux.HandleFunc("PUT /api/knowledge-bases/{name}/documents/{filename}", s.handleSaveKnowledgeDocument)
	mux.HandleFunc("POST /api/knowledge-bases/{name}/documents", s.handleUploadKnowledgeDocuments)
	mux.HandleFunc("DELETE /api/knowledge-bases/{name}/documents/{filename}", s.handleDeleteKnowledgeDocument)
	mux.HandleFunc("GET /api/executions", s.handleListExecutions)
	mux.HandleFunc("POST /api/executions", s.handleStartExecution)
	mux.HandleFunc("GET /api/executions/{id}", s.handleGetExecution)
	mux.HandleFunc("GET /api/executions/{id}/files/{name}", s.handleDownloadFile)
	mux.HandleFunc("POST /api/executions/{id}/approve", s.handleApprove)
	mux.HandleFunc("POST /api/executions/{id}/manual-edit", s.handleManualEdit)
	mux.HandleFunc("POST /api/executions/{id}/retry", s.handleRetry)
	mux.HandleFunc("POST /api/executions/{id}/restart", s.handleRestart)
	mux.HandleFunc("POST /api/executions/{id}/cancel", s.handleCancel)
	mux.HandleFunc("POST /api/executions/{id}/rerun", s.handleRerun)
	mux.HandleFunc("DELETE /api/executions/{id}", s.handleDeleteExecution)
	mux.HandleFunc("GET /api/executions/{id}/chunks", s.handleListChunks)
	mux.HandleFunc("POST /api/executions/{id}/chunks", s.handleAddChunk)
	mux.HandleFunc("POST /api/executions/{id}/chunks/{chunk}/done", s.handleCompleteChunk)
	mux.HandleFunc("DELETE /api/executions/{id}/chunks/{chunk}", s.handleRemoveChunk)
	mux.HandleFunc("GET /api/registry", s.handleRegistry)
	mux.HandleFunc("GET /api/registry/{name}", s.handleRegistryPackage)
	mux.HandleFunc("GET /api/registry/{name}/{version}", s.handleRegistryPackage)
	mux.HandleFunc("POST /api/registry/install", s.handleRegistryInstall)
	mux.HandleFunc("POST /api/registry/publish", s.handleRegistryPublish)

	return logMiddleware(mux)
}

// ListenAndServe serves HTTP and runs configured watchers until ctx is canceled.
func (s *Server) ListenAndServe(ctx context.Context, addr string) error {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	for _, cfg := range s.watchers {
		go s.watcher.Run(runCtx, cfg)
	}
	server := &http.Server{Addr: addr, Handler: s.Handler()}
	go func() {
		<-runCtx.Done()
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			log.Printf("shutting down HTTP server: %v", err)
		}
	}()
	log.Printf("SignalBox serving on http://%s", addr)
	err := server.ListenAndServe()
	cancel()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func logMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r)
		if r.URL.Path != "/api/executions" && r.URL.Path != "/" { // keep polling noise out of the log
			log.Printf("%s %s", r.Method, r.URL.Path)
		}
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	if status >= http.StatusBadRequest {
		log.Printf("HTTP %d error: %v", status, v)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	dashboard, err := s.eng.Dashboard()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, fmt.Errorf("loading dashboard: %w", err))
		return
	}
	writeJSON(w, http.StatusOK, dashboard)
}

func (s *Server) handleMeta(w http.ResponseWriter, r *http.Request) {
	limits := s.eng.UploadLimits()
	writeJSON(w, http.StatusOK, map[string]any{
		"project": s.project,
		"uploads": map[string]any{
			"max_bytes":          limits.MaxBytes,
			"max_files":          limits.MaxFiles,
			"allowed_extensions": limits.AllowedExtensions,
		},
	})
}

// pipelineView is the API projection of a pipeline definition.
type pipelineView struct {
	RegistrySources []registrySource    `json:"registry_sources,omitempty"`
	Name            string              `json:"name"`
	Description     string              `json:"description,omitempty"`
	Entry           string              `json:"entry"`
	Params          []paramView         `json:"params,omitempty"`
	Sources         []string            `json:"sources,omitempty"`
	InputSchema     string              `json:"input_schema,omitempty"`
	Files           *pipeline.FilesSpec `json:"files,omitempty"`
	Nodes           []nodeView          `json:"nodes"`
}

type paramView struct {
	Name string `json:"name"`
	pipeline.Param
}

type nodeView struct {
	Plugin          *plugin.Definition      `json:"plugin,omitempty"`
	ID              string                  `json:"id"`
	Type            string                  `json:"type"`
	Description     string                  `json:"description,omitempty"`
	Runtime         string                  `json:"runtime,omitempty"`
	Language        string                  `json:"language,omitempty"`
	CodeSource      string                  `json:"code_source,omitempty"`
	OpenSpec        *pipeline.OpenSpecSpec  `json:"openspec,omitempty"`
	TimeoutSeconds  int                     `json:"timeout_seconds,omitempty"`
	Codes           map[string]int          `json:"codes,omitempty"`
	Handoff         []handoffView           `json:"handoff,omitempty"`
	RejectTo        string                  `json:"reject_to,omitempty"`
	Retry           *pipeline.RetryPolicy   `json:"retry,omitempty"`
	MaxReworks      int                     `json:"max_reworks,omitempty"`
	Stages          []stageView             `json:"stages,omitempty"`
	Participants    []string                `json:"participants,omitempty"`
	TurnOrder       string                  `json:"turn_order,omitempty"`
	Moderator       string                  `json:"moderator,omitempty"`
	MaxRounds       int                     `json:"max_rounds,omitempty"`
	Consensus       *pipeline.Consensus     `json:"consensus,omitempty"`
	SummaryAgent    string                  `json:"summary_agent,omitempty"`
	Chunks          *pipeline.ChunkConfig   `json:"chunks,omitempty"`
	Context         *pipeline.ContextConfig `json:"context,omitempty"`
	Model           string                  `json:"model,omitempty"`
	ReasoningEffort string                  `json:"reasoning_effort,omitempty"`
	Speed           string                  `json:"speed,omitempty"`
	MCP             []pipeline.MCPUse       `json:"mcp,omitempty"`
	KnowledgeBases  []string                `json:"knowledge_bases,omitempty"`
	Pipeline        string                  `json:"pipeline,omitempty"`
	From            string                  `json:"from,omitempty"`
	Until           string                  `json:"until,omitempty"`
	Params          map[string]string       `json:"params,omitempty"`
	Output          *outputView             `json:"output,omitempty"`
	Files           string                  `json:"files,omitempty"`
	FilesBudget     int                     `json:"files_budget,omitempty"`
}

// outputView is a node's structured-output contract with defaults applied.
type outputView struct {
	Schema     string `json:"schema"`
	OnInvalid  string `json:"on_invalid"`
	MaxRepairs int    `json:"max_repairs"`
}

type handoffView struct {
	To        string `json:"to"`
	Condition string `json:"condition,omitempty"`
}

type stageView struct {
	ID     string   `json:"id"`
	Agents []string `json:"agents"`
}

func (s *Server) handlePipelines(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if err := s.refreshPipelines(); err != nil {
		writeErr(w, http.StatusInternalServerError, fmt.Errorf("reloading pipelines: %w", err))
		return
	}
	ps := s.eng.Pipelines()
	sources, _, err := s.registrySources()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	out := []pipelineView{}
	for _, name := range pipeline.Names(ps) {
		p := ps[name]
		pv := pipelineView{Name: p.Name, Description: p.Description, Entry: p.Entry, Sources: p.Sources, InputSchema: p.InputSchema(), Files: p.Files}
		pv.RegistrySources = sources[p.Name]
		for _, param := range p.ParamNames() {
			pv.Params = append(pv.Params, paramView{Name: param, Param: *p.Params[param]})
		}
		for _, n := range p.Nodes {
			nv := nodeView{
				Plugin:          n.Plugin,
				ID:              n.ID,
				Type:            string(n.Type),
				Description:     n.Description,
				Runtime:         n.Runtime,
				Language:        n.Language,
				CodeSource:      n.CodeSource,
				OpenSpec:        n.OpenSpec,
				TimeoutSeconds:  n.TimeoutSeconds,
				Codes:           n.Codes,
				RejectTo:        n.RejectTo,
				Retry:           n.Retry,
				MaxReworks:      n.MaxReworks,
				Participants:    n.Participants,
				TurnOrder:       n.TurnOrder,
				Moderator:       n.Moderator,
				MaxRounds:       n.MaxRounds,
				Consensus:       n.Consensus,
				SummaryAgent:    n.SummaryAgent,
				Chunks:          n.Chunks,
				Context:         n.Context,
				Model:           n.Model,
				ReasoningEffort: n.ReasoningEffort,
				Speed:           n.Speed,
				MCP:             n.MCP,
				KnowledgeBases:  n.KnowledgeBases,
				Pipeline:        n.Pipeline,
				From:            n.From,
				Until:           n.Until,
				Params:          n.Params,
				Files:           n.Files,
				FilesBudget:     n.FilesBudget,
			}
			if n.Output != nil {
				nv.Output = &outputView{Schema: n.Output.Schema, OnInvalid: n.InvalidPolicy(), MaxRepairs: n.RepairLimit()}
			}
			for _, route := range n.Handoff {
				nv.Handoff = append(nv.Handoff, handoffView{To: route.To, Condition: route.Condition})
			}
			for _, st := range n.Stages {
				nv.Stages = append(nv.Stages, stageView{ID: st.ID, Agents: st.Agents})
			}
			pv.Nodes = append(pv.Nodes, nv)
		}
		out = append(out, pv)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleListExecutions(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	page, err := queryInt(r, "page", 1)
	if err != nil || page < 1 {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("page must be a positive integer"))
		return
	}
	pageSize, err := queryInt(r, "page_size", 20)
	if err != nil || pageSize < 10 || pageSize > 100 || pageSize%10 != 0 {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("page_size must be a multiple of 10 between 10 and 100"))
		return
	}
	executions, total, err := s.eng.ListPage(page, pageSize)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, fmt.Errorf("listing executions: %w", err))
		return
	}
	totalPages := (total + pageSize - 1) / pageSize
	writeJSON(w, http.StatusOK, map[string]any{
		"executions":  executions,
		"page":        page,
		"page_size":   pageSize,
		"total":       total,
		"total_pages": totalPages,
	})
}

func queryInt(r *http.Request, name string, fallback int) (int, error) {
	value := r.URL.Query().Get(name)
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer", name)
	}
	return parsed, nil
}

func (s *Server) handleStartExecution(w http.ResponseWriter, r *http.Request) {
	if err := s.refreshPipelines(); err != nil {
		writeErr(w, http.StatusInternalServerError, fmt.Errorf("reloading pipelines: %w", err))
		return
	}
	var req struct {
		Pipeline string            `json:"pipeline"`
		Input    string            `json:"input"`
		Params   map[string]string `json:"params"`
	}
	var files []engine.FileInput
	if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
		var err error
		if files, err = s.readMultipartStart(w, r, &req.Pipeline, &req.Input, &req.Params); err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
	} else if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("invalid JSON body: %w", err))
		return
	}
	if req.Pipeline == "" {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("pipeline is required"))
		return
	}
	id, err := s.eng.StartWith(engine.StartOptions{Pipeline: req.Pipeline, Input: req.Input, Params: req.Params, Files: files})
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if len(files) > 0 {
		log.Printf("started execution %s (pipeline %q, %d file(s))", id, req.Pipeline, len(files))
	} else {
		log.Printf("started execution %s (pipeline %q)", id, req.Pipeline)
	}
	writeJSON(w, http.StatusCreated, map[string]string{"id": id})
}

// readMultipartStart reads a multipart start request: pipeline, input, and
// params (a JSON object) fields plus any number of "file" parts. The body
// is bounded by the upload limits before anything is buffered.
func (s *Server) readMultipartStart(w http.ResponseWriter, r *http.Request, pipelineName, input *string, params *map[string]string) ([]engine.FileInput, error) {
	limits := s.eng.UploadLimits()
	r.Body = http.MaxBytesReader(w, r.Body, limits.MaxBytes*int64(limits.MaxFiles)+(1<<20))
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return nil, fmt.Errorf("upload is larger than %d file(s) of %d bytes (uploads limits)", limits.MaxFiles, limits.MaxBytes)
		}
		return nil, fmt.Errorf("invalid multipart body: %w", err)
	}
	defer r.MultipartForm.RemoveAll()
	*pipelineName = r.FormValue("pipeline")
	*input = r.FormValue("input")
	if raw := r.FormValue("params"); raw != "" {
		if err := json.Unmarshal([]byte(raw), params); err != nil {
			return nil, fmt.Errorf("params must be a JSON object of strings: %w", err)
		}
	}
	headers := r.MultipartForm.File["file"]
	if len(headers) > limits.MaxFiles {
		return nil, fmt.Errorf("%d files attached; at most %d are allowed per execution (uploads.max_files)", len(headers), limits.MaxFiles)
	}
	files := make([]engine.FileInput, 0, len(headers))
	for _, fh := range headers {
		if fh.Size > limits.MaxBytes {
			return nil, fmt.Errorf("file %q is %d bytes; the limit is %d (uploads.max_bytes)", fh.Filename, fh.Size, limits.MaxBytes)
		}
		f, err := fh.Open()
		if err != nil {
			return nil, err
		}
		data, err := io.ReadAll(io.LimitReader(f, limits.MaxBytes+1))
		_ = f.Close()
		if err != nil {
			return nil, fmt.Errorf("reading file %q: %w", fh.Filename, err)
		}
		files = append(files, engine.FileInput{Name: fh.Filename, Data: data})
	}
	return files, nil
}

// safeDownloadTypes are served with their own content type; anything else
// (HTML, SVG, scripts) is sent as application/octet-stream.
var safeDownloadTypes = map[string]bool{
	"text/plain": true, "text/csv": true, "text/markdown": true, "text/tab-separated-values": true,
	"application/json": true, "application/yaml": true, "application/pdf": true,
	"image/png": true, "image/jpeg": true, "image/gif": true, "image/webp": true,
}

// handleDownloadFile serves one uploaded file as an attachment. Uploads are
// never rendered inline or executed.
func (s *Server) handleDownloadFile(w http.ResponseWriter, r *http.Request) {
	path, meta, err := s.eng.UploadedFile(r.PathValue("id"), r.PathValue("name"))
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	f, err := os.Open(path)
	if err != nil {
		writeErr(w, http.StatusNotFound, fmt.Errorf("file %q is no longer available", meta.Name))
		return
	}
	defer f.Close()
	ctype := "application/octet-stream"
	if base, _, _ := strings.Cut(meta.ContentType, ";"); safeDownloadTypes[strings.TrimSpace(base)] {
		ctype = meta.ContentType
	}
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": meta.Name}))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "sandbox")
	w.Header().Set("Cache-Control", "no-store")
	http.ServeContent(w, r, "", time.Time{}, f)
}

func (s *Server) handleGetExecution(w http.ResponseWriter, r *http.Request) {
	exec, err := s.eng.Get(r.PathValue("id"))
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	for _, run := range exec.Runs {
		if !run.Format || run.Output == "" {
			continue
		}
		var rendered bytes.Buffer
		if err := markdown.Convert([]byte(run.Output), &rendered); err != nil {
			writeErr(w, http.StatusInternalServerError, fmt.Errorf("rendering Markdown for %q: %w", run.NodeID, err))
			return
		}
		run.OutputHTML = rendered.String()
	}
	exec.Surfaced = s.eng.SurfacedRuns(exec.ID)
	writeJSON(w, http.StatusOK, exec)
}

func (s *Server) handleApprove(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RunID      string `json:"run_id"`
		Approved   bool   `json:"approved"`
		ApprovedBy string `json:"approved_by"`
		Comment    string `json:"comment"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("invalid JSON body: %w", err))
		return
	}
	if req.RunID == "" {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("run_id is required"))
		return
	}
	if req.ApprovedBy == "" {
		req.ApprovedBy = "web-ui"
	}
	execID := r.PathValue("id")
	if err := s.eng.Approve(execID, req.RunID, req.Approved, req.ApprovedBy, req.Comment); err != nil {
		writeErr(w, http.StatusConflict, err)
		return
	}
	verdict := "approved"
	if !req.Approved {
		verdict = "rejected"
	}
	log.Printf("execution %s: run %s %s by %s", execID, req.RunID, verdict, req.ApprovedBy)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleManualEdit(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RunID    string `json:"run_id"`
		Output   string `json:"output"`
		EditedBy string `json:"edited_by"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("invalid JSON body: %w", err))
		return
	}
	if req.RunID == "" {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("run_id is required"))
		return
	}
	if req.EditedBy == "" {
		req.EditedBy = "web-ui"
	}
	execID := r.PathValue("id")
	if err := s.eng.SubmitManualEdit(execID, req.RunID, req.Output, req.EditedBy); err != nil {
		writeErr(w, http.StatusConflict, err)
		return
	}
	log.Printf("execution %s: run %s manually edited by %s", execID, req.RunID, req.EditedBy)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleRetry(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RunID string `json:"run_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("invalid JSON body: %w", err))
		return
	}
	if req.RunID == "" {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("run_id is required"))
		return
	}
	execID := r.PathValue("id")
	if err := s.eng.Retry(execID, req.RunID); err != nil {
		writeErr(w, http.StatusConflict, err)
		return
	}
	log.Printf("execution %s: retrying run %s", execID, req.RunID)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleRestart starts a new execution from a run of a finished one. With no
// run_id or node_id the most recent failed or interrupted run is used; stage
// restarts an orchestrator run from that stage. A changed pipeline definition
// is reported as 409 with definition_changed unless force is set.
func (s *Server) handleRestart(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RunID  string `json:"run_id"`
		NodeID string `json:"node_id"`
		Stage  string `json:"stage"`
		Force  bool   `json:"force"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("invalid JSON body: %w", err))
		return
	}
	execID := r.PathValue("id")
	if _, err := s.eng.Get(execID); err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	if err := s.refreshPipelines(); err != nil {
		writeErr(w, http.StatusInternalServerError, fmt.Errorf("reloading pipelines: %w", err))
		return
	}
	newID, err := s.eng.Restart(execID, engine.RestartOptions{RunID: req.RunID, NodeID: req.NodeID, Stage: req.Stage, Force: req.Force})
	if err != nil {
		var changed *engine.DefinitionChangedError
		if errors.As(err, &changed) {
			writeJSON(w, http.StatusConflict, map[string]any{
				"error":              err.Error(),
				"definition_changed": true,
				"changes":            changed.Changes,
			})
			return
		}
		writeErr(w, http.StatusConflict, err)
		return
	}
	log.Printf("started execution %s by restarting %s", newID, execID)
	writeJSON(w, http.StatusCreated, map[string]string{"id": newID})
}

func (s *Server) handleCancel(w http.ResponseWriter, r *http.Request) {
	if err := s.eng.Cancel(r.PathValue("id")); err != nil {
		writeErr(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleRerun starts a new execution with the same pipeline and input as an
// existing one, using the pipeline definition currently on disk.
func (s *Server) handleRerun(w http.ResponseWriter, r *http.Request) {
	src, err := s.eng.Get(r.PathValue("id"))
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	if err := s.refreshPipelines(); err != nil {
		writeErr(w, http.StatusInternalServerError, fmt.Errorf("reloading pipelines: %w", err))
		return
	}
	if src.Parent != nil {
		writeErr(w, http.StatusConflict, fmt.Errorf("execution %s is part of execution %s (pipeline node %q); rerun or restart that execution instead", src.ID, src.Parent.Execution, src.Parent.NodeID))
		return
	}
	id, err := s.eng.StartWith(engine.StartOptions{Pipeline: src.Pipeline, Input: src.Input, Params: src.Params, FilesFrom: src.ID})
	if err != nil {
		writeErr(w, http.StatusConflict, err)
		return
	}
	log.Printf("started execution %s (pipeline %q, rerun of %s)", id, src.Pipeline, src.ID)
	writeJSON(w, http.StatusCreated, map[string]string{"id": id})
}

func (s *Server) handleDeleteExecution(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := s.eng.Get(id); err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	if err := s.eng.Delete(id); err != nil {
		writeErr(w, http.StatusConflict, err)
		return
	}
	log.Printf("deleted execution %s", id)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleListChunks(w http.ResponseWriter, r *http.Request) {
	chunks, err := s.eng.ListChunks(r.PathValue("id"))
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, http.StatusOK, chunks)
}

func (s *Server) handleAddChunk(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Title string `json:"title"`
		By    string `json:"by"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("invalid JSON body: %w", err))
		return
	}
	id := r.PathValue("id")
	if _, err := s.eng.Get(id); err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	if req.By == "" {
		req.By = "web-ui"
	}
	chunk, err := s.eng.AddChunk(id, req.Title, req.By)
	if err != nil {
		writeChunkErr(w, err)
		return
	}
	log.Printf("execution %s: chunk %s added by %s", id, chunk.ID, req.By)
	writeJSON(w, http.StatusCreated, chunk)
}

func (s *Server) handleCompleteChunk(w http.ResponseWriter, r *http.Request) {
	var req struct {
		By string `json:"by"`
	}
	if r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, http.StatusBadRequest, fmt.Errorf("invalid JSON body: %w", err))
			return
		}
	}
	id := r.PathValue("id")
	if _, err := s.eng.Get(id); err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	if req.By == "" {
		req.By = "web-ui"
	}
	if err := s.eng.CompleteChunk(id, r.PathValue("chunk"), req.By); err != nil {
		writeChunkErr(w, err)
		return
	}
	log.Printf("execution %s: chunk %s marked done by %s", id, r.PathValue("chunk"), req.By)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleRemoveChunk(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := s.eng.Get(id); err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	if err := s.eng.RemoveChunk(id, r.PathValue("chunk")); err != nil {
		writeChunkErr(w, err)
		return
	}
	log.Printf("execution %s: chunk %s removed", id, r.PathValue("chunk"))
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func writeChunkErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, engine.ErrChunksReadOnly):
		writeErr(w, http.StatusConflict, err)
	case strings.Contains(err.Error(), "unknown chunk"):
		writeErr(w, http.StatusNotFound, err)
	default:
		writeErr(w, http.StatusBadRequest, err)
	}
}
