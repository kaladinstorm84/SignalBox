package registry

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/kaladinstorm84/SignalBox/internal/config"
	"github.com/kaladinstorm84/SignalBox/internal/knowledgebase"
	"github.com/kaladinstorm84/SignalBox/internal/pipeline"
)

// PublishKnowledge publishes all text documents in a project knowledge base.
func (r *Registry) PublishKnowledge(cfg *config.Config, name, description string, opts PublishOptions) (*Manifest, error) {
	if !ValidName(name) {
		return nil, fmt.Errorf("knowledge base name %q cannot be published: use 1-64 lowercase letters, digits, '_' or '-', excluding device names", name)
	}
	if err := r.checkNameClash(name); err != nil {
		return nil, err
	}
	existing, _ := r.Versions(name)
	if len(existing) > 0 {
		previous, err := r.Manifest(name, existing[0])
		if err != nil {
			return nil, err
		}
		if previous.PackageKind() != KindKnowledge {
			return nil, fmt.Errorf("package %q is already used by a pipeline; choose a different name", name)
		}
	}
	version := opts.Version
	if version == "" {
		version = "0.1.0"
		if len(existing) > 0 {
			version = pipeline.NextPatch(existing[0])
		}
	}
	if !pipeline.ValidVersion(version) {
		return nil, fmt.Errorf("version %q must be a semantic version such as 1.2.0", version)
	}
	for _, v := range existing {
		if v == version {
			return nil, fmt.Errorf("%s@%s is already published; versions are immutable (pass --version)", name, version)
		}
	}
	contents, err := knowledgeDocuments(cfg.Root, name)
	if err != nil {
		return nil, err
	}
	m := &Manifest{
		Kind: KindKnowledge, Name: name, Version: version, Description: description,
		SchemaVersion: pipeline.SchemaVersion,
		PublishedAt:   time.Now().UTC().Truncate(time.Second), PublishedBy: opts.By,
		Entry: sortedKeys(contents)[0],
	}
	return r.publishContents(m, contents)
}

func knowledgeDocuments(root, name string) (map[string][]byte, error) {
	bases, err := knowledgebase.New(root).List()
	if err != nil {
		return nil, err
	}
	for _, base := range bases {
		if base.Name != name {
			continue
		}
		if len(base.Documents) == 0 {
			return nil, fmt.Errorf("knowledge base %q has no text documents to publish", name)
		}
		contents := make(map[string][]byte, len(base.Documents))
		for _, doc := range base.Documents {
			rel := path.Join("knowledge-bases", name, doc.Name)
			if err := safeRel(rel); err != nil {
				return nil, err
			}
			data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
			if err != nil {
				return nil, err
			}
			if err := validateKnowledgeDocument(rel, data); err != nil {
				return nil, err
			}
			contents[rel] = data
		}
		return contents, nil
	}
	return nil, fmt.Errorf("knowledge base %q does not exist", name)
}

func validateKnowledgeDocument(rel string, data []byte) error {
	if !knowledgebase.ValidDocumentName(path.Base(rel)) || strings.ContainsAny(path.Base(rel), `:<>"|?*`) {
		return fmt.Errorf("knowledge document %q must be a portable plain-text filename", rel)
	}
	if len(data) > knowledgebase.MaxDocumentBytes || !utf8.Valid(data) {
		return fmt.Errorf("knowledge document %q must contain UTF-8 text of at most %d bytes", rel, knowledgebase.MaxDocumentBytes)
	}
	return nil
}

func validateKnowledgePackage(m *Manifest, contents map[string][]byte) error {
	dir := path.Join("knowledge-bases", m.Name)
	seen := map[string]bool{}
	for _, f := range m.Files {
		if path.Dir(f.Path) != dir {
			return fmt.Errorf("knowledge package %s@%s may only contain documents in %s", m.Name, m.Version, dir)
		}
		key := strings.ToLower(f.Path)
		if seen[key] {
			return fmt.Errorf("knowledge package repeats document %q", f.Path)
		}
		seen[key] = true
		if err := validateKnowledgeDocument(f.Path, contents[f.Path]); err != nil {
			return err
		}
	}
	return nil
}

func checkKnowledgeTarget(root, rel string) error {
	current := root
	for _, part := range strings.Split(rel, "/") {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("knowledge install target %s must not be a symbolic link", current)
		}
	}
	return nil
}

// PublishableKnowledge lists local knowledge bases with portable package names.
func PublishableKnowledge(cfg *config.Config) ([]string, error) {
	bases, err := knowledgebase.New(cfg.Root).List()
	if err != nil {
		return nil, err
	}
	names := []string{}
	for _, base := range bases {
		if ValidName(base.Name) && len(base.Documents) > 0 {
			names = append(names, base.Name)
		}
	}
	return names, nil
}
