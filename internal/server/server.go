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
	"io/fs"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/kaladinstorm84/SignalBox/internal/config"
	"github.com/kaladinstorm84/SignalBox/internal/engine"
	"github.com/kaladinstorm84/SignalBox/internal/pipeline"
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
	mux.HandleFunc("GET /api/settings", s.handleGetSettings)
	mux.HandleFunc("PUT /api/settings", s.handleSaveSettings)
	mux.HandleFunc("GET /api/pipelines", s.handlePipelines)
	mux.HandleFunc("GET /api/pipeline-files", s.handleListPipelineFiles)
	mux.HandleFunc("GET /api/pipeline-files/{filename}", s.handleGetPipelineFile)
	mux.HandleFunc("PUT /api/pipeline-files/{filename}", s.handleSavePipelineFile)
	mux.HandleFunc("GET /api/executions", s.handleListExecutions)
	mux.HandleFunc("POST /api/executions", s.handleStartExecution)
	mux.HandleFunc("GET /api/executions/{id}", s.handleGetExecution)
	mux.HandleFunc("POST /api/executions/{id}/approve", s.handleApprove)
	mux.HandleFunc("POST /api/executions/{id}/retry", s.handleRetry)
	mux.HandleFunc("POST /api/executions/{id}/restart", s.handleRestartInterruptedRun)
	mux.HandleFunc("POST /api/executions/{id}/cancel", s.handleCancel)
	mux.HandleFunc("POST /api/executions/{id}/rerun", s.handleRerun)
	mux.HandleFunc("DELETE /api/executions/{id}", s.handleDeleteExecution)

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
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

func (s *Server) handleMeta(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"project": s.project})
}

// pipelineView is the API projection of a pipeline definition.
type pipelineView struct {
	Name        string     `json:"name"`
	Description string     `json:"description,omitempty"`
	Entry       string     `json:"entry"`
	Nodes       []nodeView `json:"nodes"`
}

type nodeView struct {
	ID             string         `json:"id"`
	Type           string         `json:"type"`
	Description    string         `json:"description,omitempty"`
	Runtime        string         `json:"runtime,omitempty"`
	Language       string         `json:"language,omitempty"`
	CodeSource     string         `json:"code_source,omitempty"`
	TimeoutSeconds int            `json:"timeout_seconds,omitempty"`
	Codes          map[string]int `json:"codes,omitempty"`
	Handoff        []string       `json:"handoff,omitempty"`
	RejectTo       string         `json:"reject_to,omitempty"`
	Stages         []stageView    `json:"stages,omitempty"`
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
	out := []pipelineView{}
	for _, name := range pipeline.Names(ps) {
		p := ps[name]
		pv := pipelineView{Name: p.Name, Description: p.Description, Entry: p.Entry}
		for _, n := range p.Nodes {
			nv := nodeView{
				ID:             n.ID,
				Type:           string(n.Type),
				Description:    n.Description,
				Runtime:        n.Runtime,
				Language:       n.Language,
				CodeSource:     n.CodeSource,
				TimeoutSeconds: n.TimeoutSeconds,
				Codes:          n.Codes,
				Handoff:        n.Handoff,
				RejectTo:       n.RejectTo,
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
	writeJSON(w, http.StatusOK, s.eng.List())
}

func (s *Server) handleStartExecution(w http.ResponseWriter, r *http.Request) {
	if err := s.refreshPipelines(); err != nil {
		writeErr(w, http.StatusInternalServerError, fmt.Errorf("reloading pipelines: %w", err))
		return
	}
	var req struct {
		Pipeline string `json:"pipeline"`
		Input    string `json:"input"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("invalid JSON body: %w", err))
		return
	}
	if req.Pipeline == "" {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("pipeline is required"))
		return
	}
	id, err := s.eng.Start(req.Pipeline, req.Input)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	log.Printf("started execution %s (pipeline %q)", id, req.Pipeline)
	writeJSON(w, http.StatusCreated, map[string]string{"id": id})
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

func (s *Server) handleRestartInterruptedRun(w http.ResponseWriter, r *http.Request) {
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
	if err := s.refreshPipelines(); err != nil {
		writeErr(w, http.StatusInternalServerError, fmt.Errorf("reloading pipelines: %w", err))
		return
	}
	newID, err := s.eng.RestartFromInterrupted(r.PathValue("id"), req.RunID)
	if err != nil {
		writeErr(w, http.StatusConflict, err)
		return
	}
	log.Printf("started execution %s from interrupted run %s", newID, req.RunID)
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
	id, err := s.eng.Start(src.Pipeline, src.Input)
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
