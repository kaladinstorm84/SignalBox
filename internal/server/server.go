// Package server exposes the web UI and JSON API for tracking executions and
// starting new pipeline runs.
package server

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"net/http"

	"github.com/gitea/signalbox/internal/engine"
	"github.com/gitea/signalbox/internal/pipeline"
)

//go:embed ui
var uiFS embed.FS

// Server wires the engine to HTTP handlers.
type Server struct {
	eng     *engine.Engine
	project string
}

// New creates a server for the given engine.
func New(eng *engine.Engine, project string) *Server {
	return &Server{eng: eng, project: project}
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
	mux.HandleFunc("GET /api/pipelines", s.handlePipelines)
	mux.HandleFunc("GET /api/executions", s.handleListExecutions)
	mux.HandleFunc("POST /api/executions", s.handleStartExecution)
	mux.HandleFunc("GET /api/executions/{id}", s.handleGetExecution)
	mux.HandleFunc("POST /api/executions/{id}/approve", s.handleApprove)
	mux.HandleFunc("POST /api/executions/{id}/cancel", s.handleCancel)

	return logMiddleware(mux)
}

// ListenAndServe blocks serving HTTP on addr.
func (s *Server) ListenAndServe(addr string) error {
	log.Printf("SignalBox serving on http://%s", addr)
	return http.ListenAndServe(addr, s.Handler())
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
	ID          string      `json:"id"`
	Type        string      `json:"type"`
	Description string      `json:"description,omitempty"`
	Runtime     string      `json:"runtime,omitempty"`
	Handoff     []string    `json:"handoff,omitempty"`
	Stages      []stageView `json:"stages,omitempty"`
}

type stageView struct {
	ID     string   `json:"id"`
	Agents []string `json:"agents"`
}

func (s *Server) handlePipelines(w http.ResponseWriter, r *http.Request) {
	ps := s.eng.Pipelines()
	out := []pipelineView{}
	for _, name := range pipeline.Names(ps) {
		p := ps[name]
		pv := pipelineView{Name: p.Name, Description: p.Description, Entry: p.Entry}
		for _, n := range p.Nodes {
			nv := nodeView{
				ID:          n.ID,
				Type:        string(n.Type),
				Description: n.Description,
				Runtime:     n.Runtime,
				Handoff:     n.Handoff,
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

func (s *Server) handleCancel(w http.ResponseWriter, r *http.Request) {
	if err := s.eng.Cancel(r.PathValue("id")); err != nil {
		writeErr(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
