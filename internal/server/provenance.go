package server

import (
	"fmt"
	"sort"
	"strings"

	"github.com/kaladinstorm84/SignalBox/internal/registry"
)

type registrySource struct {
	Package string `json:"package"`
	Kind    string `json:"kind"`
	Version string `json:"version"`
}

func (s *Server) registrySources() (map[string][]registrySource, map[string][]registrySource, error) {
	pipelines := map[string][]registrySource{}
	knowledge := map[string][]registrySource{}
	if s.projectRoot == "" {
		return pipelines, knowledge, nil
	}
	lock, err := registry.ReadLock(s.projectRoot)
	if err != nil {
		return nil, nil, fmt.Errorf("loading registry provenance: %w", err)
	}
	names := make([]string, 0, len(lock.Packages))
	for name := range lock.Packages {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		entry := lock.Packages[name]
		kind := entry.Kind
		if kind == "" {
			kind = registry.KindPipeline
		}
		source := registrySource{Package: name, Kind: kind, Version: entry.Version}
		if kind == registry.KindPipeline {
			pipelines[name] = append(pipelines[name], source)
		}
		seen := map[string]bool{}
		for _, file := range entry.Files {
			parts := strings.Split(file.Path, "/")
			if len(parts) == 3 && parts[0] == "knowledge-bases" && !seen[parts[1]] {
				knowledge[parts[1]] = append(knowledge[parts[1]], source)
				seen[parts[1]] = true
			}
		}
	}
	return pipelines, knowledge, nil
}
