package server

import (
	"fmt"
	"log"
	"mime"
	"net/http"
	"strings"

	"github.com/kaladinstorm84/SignalBox/internal/exporter"
	"github.com/kaladinstorm84/SignalBox/internal/runtime"
)

func (s *Server) handleExportPipeline(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if format := r.URL.Query().Get("format"); format != "" && format != "python" {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("unsupported export format %q (want python)", format))
		return
	}
	cfg, err := s.pipelineFileConfig()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	pipelines, err := s.loadPipelines()
	if err != nil {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("loading pipelines for export: %w", err))
		return
	}
	p, ok := pipelines[r.PathValue("name")]
	if !ok {
		writeErr(w, http.StatusNotFound, fmt.Errorf("unknown pipeline %q", r.PathValue("name")))
		return
	}
	if err := runtime.ValidatePipeline(cfg, p); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	data, err := exporter.Python(p, cfg)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	filename := strings.Map(func(r rune) rune {
		if strings.ContainsRune(`<>:"/\|?*`, r) || r < 32 {
			return '_'
		}
		return r
	}, p.Name) + ".py"
	w.Header().Set("Content-Type", "text/x-python; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": filename}))
	if _, err := w.Write(data); err != nil {
		log.Printf("writing Python export for pipeline %q: %v", p.Name, err)
	}
}
