package server

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"

	"github.com/kaladinstorm84/SignalBox/internal/config"
	"github.com/kaladinstorm84/SignalBox/internal/registry"
)

func (s *Server) openRegistry() (*config.Config, *registry.Registry, error) {
	cfg, err := s.pipelineFileConfig()
	if err != nil {
		return nil, nil, err
	}
	reg, err := registry.Open(cfg.RegistryPath())
	if err != nil {
		return cfg, nil, err
	}
	return cfg, reg, nil
}

// handleRegistry searches the configured registry and reports installed and
// publishable pipelines. An unconfigured registry is not an error.
func (s *Server) handleRegistry(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	cfg, err := s.pipelineFileConfig()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	lock, err := registry.ReadLock(cfg.Root)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	publishable, _ := registry.Publishable(cfg)
	resp := map[string]any{
		"configured":  cfg.Registry.Path != "",
		"path":        cfg.RegistryPath(),
		"installed":   lock.Packages,
		"publishable": publishable,
		"packages":    []registry.Summary{},
	}
	if cfg.Registry.Path != "" {
		reg, err := registry.Open(cfg.RegistryPath())
		if err != nil {
			resp["error"] = err.Error()
		} else if packages, err := reg.Search(r.URL.Query().Get("q")); err != nil {
			resp["error"] = err.Error()
		} else {
			resp["packages"] = packages
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleRegistryPackage returns a manifest, its sibling versions, and the
// verified contents of the package's text files for inspection.
func (s *Server) handleRegistryPackage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	_, reg, err := s.openRegistry()
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	ref := r.PathValue("name")
	if v := r.PathValue("version"); v != "" && v != "latest" {
		ref += "@" + v
	}
	name, version, err := reg.Resolve(ref)
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	m, err := reg.Manifest(name, version)
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	versions, _ := reg.Versions(name)
	files := map[string]string{}
	verifyErr := ""
	for _, f := range m.Files {
		data, err := reg.ReadFile(m, f.Path)
		if err != nil {
			verifyErr = err.Error()
			continue
		}
		files[f.Path] = string(data)
	}
	if verifyErr == "" {
		if _, err := reg.Verify(m); err != nil {
			verifyErr = err.Error()
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"manifest":     m,
		"versions":     versions,
		"files":        files,
		"verify_error": verifyErr,
	})
}

func (s *Server) handleRegistryInstall(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Ref    string `json:"ref"`
		Digest string `json:"digest"`
		Force  bool   `json:"force"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Ref == "" {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("ref (name or name@version) is required"))
		return
	}
	s.settingsMu.Lock()
	defer s.settingsMu.Unlock()
	cfg, reg, err := s.openRegistry()
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	result, err := reg.Install(cfg, req.Ref, registry.InstallOptions{Digest: req.Digest, Force: req.Force})
	if err != nil {
		writeErr(w, http.StatusConflict, err)
		return
	}
	if err := s.refreshPipelines(); err != nil {
		writeErr(w, http.StatusInternalServerError, fmt.Errorf("installed, but reloading pipelines failed: %w", err))
		return
	}
	log.Printf("installed %s@%s from registry", result.Manifest.Name, result.Manifest.Version)
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleRegistryPublish(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Pipeline string `json:"pipeline"`
		Version  string `json:"version"`
		By       string `json:"by"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Pipeline == "" {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("pipeline is required"))
		return
	}
	s.settingsMu.Lock()
	defer s.settingsMu.Unlock()
	cfg, reg, err := s.openRegistry()
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	m, err := reg.Publish(cfg, req.Pipeline, registry.PublishOptions{Version: req.Version, By: req.By})
	if err != nil {
		writeErr(w, http.StatusConflict, err)
		return
	}
	log.Printf("published %s@%s to registry", m.Name, m.Version)
	writeJSON(w, http.StatusCreated, m)
}
