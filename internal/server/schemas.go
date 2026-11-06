package server

import (
	"net/http"
	"path/filepath"

	"github.com/kaladinstorm84/SignalBox/internal/config"
	"github.com/kaladinstorm84/SignalBox/internal/pipeline"
	"github.com/kaladinstorm84/SignalBox/internal/schema"
)

type schemaView struct {
	*schema.Schema
	// UsedBy lists "pipeline/agent" (or "pipeline/input") references.
	UsedBy []string `json:"used_by,omitempty"`
}

type schemasResponse struct {
	Dir     string       `json:"dir"`
	Schemas []schemaView `json:"schemas"`
	Errors  []string     `json:"errors,omitempty"`
}

// handleSchemas lists the project's JSON Schema files and which pipelines
// use them.
func (s *Server) handleSchemas(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	out := schemasResponse{Schemas: []schemaView{}}
	if s.projectRoot == "" {
		writeJSON(w, http.StatusOK, out)
		return
	}
	cfg, err := config.Load(s.projectRoot)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	set, errs := schema.LoadDir(cfg.SchemasPath())
	if rel, err := filepath.Rel(cfg.Root, cfg.SchemasPath()); err == nil {
		out.Dir = filepath.ToSlash(rel)
	}
	for _, err := range errs {
		out.Errors = append(out.Errors, err.Error())
	}
	usedBy := map[string][]string{}
	ps := s.eng.Pipelines()
	for _, name := range pipeline.Names(ps) {
		p := ps[name]
		if in := p.InputSchema(); in != "" {
			usedBy[in] = append(usedBy[in], p.Name+"/input")
		}
		for _, n := range p.Nodes {
			if sch := n.OutputSchema(); sch != "" {
				usedBy[sch] = append(usedBy[sch], p.Name+"/"+n.ID)
			}
		}
	}
	for _, sch := range set.List() {
		out.Schemas = append(out.Schemas, schemaView{Schema: sch, UsedBy: usedBy[sch.Name]})
	}
	writeJSON(w, http.StatusOK, out)
}
