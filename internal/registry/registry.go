// Package registry shares pipelines through a directory (or file share)
// registry. Each published version is an immutable package:
//
//	<registry>/<name>/<version>/manifest.json
//	<registry>/<name>/<version>/files/<project-relative path>
//
// The manifest records a sha256 for every file and a digest over all of them,
// so installs can verify integrity and pin an exact package.
package registry

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/kaladinstorm84/SignalBox/internal/config"
	"github.com/kaladinstorm84/SignalBox/internal/pipeline"
	"github.com/kaladinstorm84/SignalBox/internal/runtime"
)

// ManifestFile is the manifest name inside each package version directory.
const ManifestFile = "manifest.json"

// LockFile records installed packages at the project root.
const LockFile = "signalbox.lock.json"

// Package names are directory names on whatever file share hosts the
// registry, so they are restricted to a portable subset: lowercase (no
// case-only clashes on Windows/macOS), no dots, and no reserved device names.
var namePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

var reservedNames = map[string]bool{"con": true, "prn": true, "aux": true, "nul": true,
	"com1": true, "com2": true, "com3": true, "com4": true, "com5": true, "com6": true, "com7": true, "com8": true, "com9": true,
	"lpt1": true, "lpt2": true, "lpt3": true, "lpt4": true, "lpt5": true, "lpt6": true, "lpt7": true, "lpt8": true, "lpt9": true}

// File is one file in a package, stored by its project-relative path.
type File struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

// Manifest describes one published package version.
type Manifest struct {
	Name          string    `json:"name"`
	Version       string    `json:"version"`
	Description   string    `json:"description,omitempty"`
	SchemaVersion int       `json:"schema_version"`
	PublishedAt   time.Time `json:"published_at"`
	PublishedBy   string    `json:"published_by,omitempty"`
	// Entry is the pipeline file's path; PipelinesDir is the publisher's
	// pipelines directory, so installs can map it to the consumer's.
	Entry        string   `json:"entry"`
	PipelinesDir string   `json:"pipelines_dir"`
	Runtimes     []string `json:"runtimes,omitempty"`
	Params       []string `json:"params,omitempty"`
	Files        []File   `json:"files"`
	Digest       string   `json:"digest"`
}

// Summary is one package in search results.
type Summary struct {
	Name        string   `json:"name"`
	Latest      string   `json:"latest"`
	Description string   `json:"description,omitempty"`
	Versions    []string `json:"versions"`
}

// Registry is a directory of packages.
type Registry struct {
	Dir string
}

// Open returns the registry at dir, which must exist.
func Open(dir string) (*Registry, error) {
	if dir == "" {
		return nil, fmt.Errorf("no registry configured (set registry.path in %s or pass --registry)", config.FileName)
	}
	info, err := os.Stat(dir)
	if err != nil {
		return nil, fmt.Errorf("registry %s: %w", dir, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("registry %s is not a directory", dir)
	}
	return &Registry{Dir: dir}, nil
}

// ValidName reports whether name can be used as a package name.
func ValidName(name string) bool {
	return namePattern.MatchString(name) && !reservedNames[name]
}

// Digest is the sha256 over the sorted "path:sha256" lines of files.
func Digest(files []File) string {
	lines := make([]string, len(files))
	for i, f := range files {
		lines[i] = f.Path + ":" + f.SHA256
	}
	sort.Strings(lines)
	sum := sha256.Sum256([]byte(strings.Join(lines, "\n")))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func hashBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// Versions lists a package's published versions, newest first.
func (r *Registry) Versions(name string) ([]string, error) {
	if !ValidName(name) {
		return nil, fmt.Errorf("invalid package name %q", name)
	}
	entries, err := os.ReadDir(filepath.Join(r.Dir, name))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("package %q not found in registry", name)
		}
		return nil, err
	}
	var versions []string
	for _, e := range entries {
		if e.IsDir() && pipeline.ValidVersion(e.Name()) {
			if _, err := os.Stat(filepath.Join(r.Dir, name, e.Name(), ManifestFile)); err == nil {
				versions = append(versions, e.Name())
			}
		}
	}
	if len(versions) == 0 {
		return nil, fmt.Errorf("package %q has no published versions", name)
	}
	sort.Slice(versions, func(i, j int) bool { return pipeline.CompareVersions(versions[i], versions[j]) > 0 })
	return versions, nil
}

// Resolve splits "name[@version]" and picks the newest version when none is
// given.
func (r *Registry) Resolve(ref string) (string, string, error) {
	name, version, _ := strings.Cut(ref, "@")
	versions, err := r.Versions(name)
	if err != nil {
		return "", "", err
	}
	if version == "" || version == "latest" {
		return name, versions[0], nil
	}
	for _, v := range versions {
		if v == version {
			return name, v, nil
		}
	}
	return "", "", fmt.Errorf("package %s has no version %s (available: %s)", name, version, strings.Join(versions, ", "))
}

// Manifest reads a package version's manifest.
func (r *Registry) Manifest(name, version string) (*Manifest, error) {
	if !ValidName(name) || !pipeline.ValidVersion(version) {
		return nil, fmt.Errorf("invalid package reference %s@%s", name, version)
	}
	data, err := os.ReadFile(filepath.Join(r.Dir, name, version, ManifestFile))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("package %s@%s not found in registry", name, version)
		}
		return nil, err
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("package %s@%s: invalid manifest: %w", name, version, err)
	}
	if m.Name != name || m.Version != version {
		return nil, fmt.Errorf("package %s@%s: manifest names %s@%s", name, version, m.Name, m.Version)
	}
	return &m, nil
}

// Search lists packages whose name or latest description contains query
// (case-insensitive); an empty query lists everything.
func (r *Registry) Search(query string) ([]Summary, error) {
	entries, err := os.ReadDir(r.Dir)
	if err != nil {
		return nil, err
	}
	query = strings.ToLower(strings.TrimSpace(query))
	out := []Summary{}
	for _, e := range entries {
		if !e.IsDir() || !ValidName(e.Name()) {
			continue
		}
		versions, err := r.Versions(e.Name())
		if err != nil {
			continue
		}
		m, err := r.Manifest(e.Name(), versions[0])
		if err != nil {
			continue
		}
		if query != "" && !strings.Contains(strings.ToLower(m.Name+" "+m.Description), query) {
			continue
		}
		out = append(out, Summary{Name: m.Name, Latest: m.Version, Description: m.Description, Versions: versions})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// ReadFile returns one verified file from a package.
func (r *Registry) ReadFile(m *Manifest, rel string) ([]byte, error) {
	for _, f := range m.Files {
		if f.Path != rel {
			continue
		}
		if err := safeRel(rel); err != nil {
			return nil, fmt.Errorf("package %s@%s: %w", m.Name, m.Version, err)
		}
		data, err := os.ReadFile(filepath.Join(r.Dir, m.Name, m.Version, "files", filepath.FromSlash(rel)))
		if err != nil {
			return nil, err
		}
		if hashBytes(data) != f.SHA256 {
			return nil, fmt.Errorf("package %s@%s: %s does not match its sha256 (registry tampered or corrupt)", m.Name, m.Version, rel)
		}
		return data, nil
	}
	return nil, fmt.Errorf("package %s@%s has no file %s", m.Name, m.Version, rel)
}

// Verify checks the schema, every file hash, and the digest, returning the
// file contents by path.
func (r *Registry) Verify(m *Manifest) (map[string][]byte, error) {
	if m.SchemaVersion > pipeline.SchemaVersion {
		return nil, fmt.Errorf("package %s@%s needs pipeline schema %d; this SignalBox supports %d (upgrade SignalBox)", m.Name, m.Version, m.SchemaVersion, pipeline.SchemaVersion)
	}
	if got := Digest(m.Files); got != m.Digest {
		return nil, fmt.Errorf("package %s@%s: manifest digest %s does not match its files (%s)", m.Name, m.Version, m.Digest, got)
	}
	contents := map[string][]byte{}
	hasEntry := false
	for _, f := range m.Files {
		data, err := r.ReadFile(m, f.Path)
		if err != nil {
			return nil, err
		}
		contents[f.Path] = data
		hasEntry = hasEntry || f.Path == m.Entry
	}
	if !hasEntry {
		return nil, fmt.Errorf("package %s@%s: entry %s is not among its files", m.Name, m.Version, m.Entry)
	}
	return contents, nil
}

// safeRel rejects absolute paths and paths escaping the project root.
func safeRel(rel string) error {
	clean := path.Clean(rel)
	if rel == "" || path.IsAbs(rel) || clean != rel || clean == ".." || strings.HasPrefix(clean, "../") || strings.ContainsAny(rel, `\:`) {
		return fmt.Errorf("unsafe file path %q", rel)
	}
	return nil
}

// checkNameClash refuses a name whose directory differs only by case from
// one already in the registry (possible on case-sensitive shares populated
// by hand), since it would merge with it on case-insensitive filesystems.
func (r *Registry) checkNameClash(name string) error {
	entries, err := os.ReadDir(r.Dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.IsDir() && e.Name() != name && strings.EqualFold(e.Name(), name) {
			return fmt.Errorf("registry already has %q, which differs from %q only by case", e.Name(), name)
		}
	}
	return nil
}

// PublishOptions controls Publish.
type PublishOptions struct {
	// Version overrides the pipeline's version; empty uses the pipeline's
	// version: field, else the next patch after the newest published.
	Version string
	By      string
}

// Publish packages one project pipeline (with its imports and code files)
// as a new immutable version.
func (r *Registry) Publish(cfg *config.Config, name string, opts PublishOptions) (*Manifest, error) {
	if !ValidName(name) {
		return nil, fmt.Errorf("pipeline name %q cannot be published: package names must be 1-64 lowercase letters, digits, '_' or '-' (they become directory names on the registry share)", name)
	}
	if err := r.checkNameClash(name); err != nil {
		return nil, err
	}
	root, err := filepath.Abs(cfg.Root)
	if err != nil {
		return nil, err
	}
	file, p, err := findPipeline(cfg, name)
	if err != nil {
		return nil, err
	}
	if err := runtime.ValidatePipeline(cfg, p); err != nil {
		return nil, err
	}

	version := opts.Version
	if version == "" {
		version = p.Version
	}
	existing, _ := r.Versions(name)
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
			return nil, fmt.Errorf("%s@%s is already published; versions are immutable (bump version: or pass --version)", name, version)
		}
	}

	rels := map[string]bool{}
	add := func(abs, what string) error {
		rel, err := filepath.Rel(root, abs)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
			return fmt.Errorf("%s %s is outside the project and cannot be published", what, abs)
		}
		rels[filepath.ToSlash(rel)] = true
		return nil
	}
	if err := add(file, "pipeline"); err != nil {
		return nil, err
	}
	for _, source := range p.Sources {
		abs := source
		if !filepath.IsAbs(abs) {
			abs = filepath.Join(root, filepath.FromSlash(source))
		}
		if err := add(abs, "import"); err != nil {
			return nil, err
		}
	}
	runtimes := map[string]bool{}
	for i := range p.Nodes {
		n := &p.Nodes[i]
		if n.Runtime != "" {
			runtimes[n.Runtime] = true
		}
		if n.Type == pipeline.TypeCode {
			abs := n.CodeSource
			if !filepath.IsAbs(abs) {
				abs = filepath.Join(root, abs)
			}
			if err := add(abs, "code-source"); err != nil {
				return nil, err
			}
		}
	}

	pipelinesDir, err := filepath.Rel(root, cfg.PipelinesPath())
	if err != nil {
		return nil, err
	}
	m := &Manifest{
		Name:          name,
		Version:       version,
		Description:   p.Description,
		SchemaVersion: pipeline.SchemaVersion,
		PublishedAt:   time.Now().UTC().Truncate(time.Second),
		PublishedBy:   opts.By,
		Entry:         filepath.ToSlash(mustRel(root, file)),
		PipelinesDir:  filepath.ToSlash(pipelinesDir),
		Runtimes:      sortedKeys(runtimes),
		Params:        sortedKeys(p.Params),
	}
	pkgDir := filepath.Join(r.Dir, name)
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		return nil, err
	}
	tmp, err := os.MkdirTemp(pkgDir, ".publish-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	for _, rel := range sortedKeys(rels) {
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			return nil, err
		}
		dst := filepath.Join(tmp, "files", filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return nil, err
		}
		if err := os.WriteFile(dst, data, 0o644); err != nil {
			return nil, err
		}
		m.Files = append(m.Files, File{Path: rel, SHA256: hashBytes(data), Size: int64(len(data))})
	}
	m.Digest = Digest(m.Files)
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(tmp, ManifestFile), append(data, '\n'), 0o644); err != nil {
		return nil, err
	}
	if err := os.Rename(tmp, filepath.Join(pkgDir, version)); err != nil {
		if _, statErr := os.Stat(filepath.Join(pkgDir, version)); statErr == nil {
			return nil, fmt.Errorf("%s@%s is already published", name, version)
		}
		return nil, err
	}
	return m, nil
}

// InstallOptions controls Install.
type InstallOptions struct {
	// Digest pins the exact package; installation fails if it differs.
	Digest string
	// Force overwrites project files that differ from the package.
	Force bool
}

// InstallResult reports what an install wrote.
type InstallResult struct {
	Manifest *Manifest `json:"manifest"`
	Written  []string  `json:"written"`
	Skipped  []string  `json:"unchanged"`
	Replaced []string  `json:"replaced,omitempty"`
}

// Install verifies a package and copies it into the project, after checking
// it validates against the project's runtimes and does not clash with
// existing pipelines or locally modified files.
func (r *Registry) Install(cfg *config.Config, ref string, opts InstallOptions) (*InstallResult, error) {
	name, version, err := r.Resolve(ref)
	if err != nil {
		return nil, err
	}
	m, err := r.Manifest(name, version)
	if err != nil {
		return nil, err
	}
	if opts.Digest != "" && opts.Digest != m.Digest {
		return nil, fmt.Errorf("package %s@%s has digest %s, not the pinned %s", name, version, m.Digest, opts.Digest)
	}
	contents, err := r.Verify(m)
	if err != nil {
		return nil, err
	}
	root, err := filepath.Abs(cfg.Root)
	if err != nil {
		return nil, err
	}
	pipelinesDir, err := filepath.Rel(root, cfg.PipelinesPath())
	if err != nil {
		return nil, err
	}
	pipelinesDir = path.Clean(filepath.ToSlash(pipelinesDir))
	targets := map[string]string{}
	for rel := range contents {
		target := mapPath(rel, m.PipelinesDir, pipelinesDir)
		if err := safeRel(target); err != nil {
			return nil, fmt.Errorf("package %s@%s: %w", name, version, err)
		}
		targets[rel] = target
	}
	entryTarget := targets[m.Entry]
	if path.Dir(entryTarget) != pipelinesDir {
		return nil, fmt.Errorf("package %s@%s: entry %s would not land in the pipelines directory", name, version, m.Entry)
	}

	// Validate the package in a staging copy before touching the project.
	stage, err := os.MkdirTemp("", "signalbox-install-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(stage)
	for rel, data := range contents {
		dst := filepath.Join(stage, filepath.FromSlash(targets[rel]))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return nil, err
		}
		if err := os.WriteFile(dst, data, 0o644); err != nil {
			return nil, err
		}
	}
	stageCfg := *cfg
	stageCfg.Root = stage
	p, err := pipeline.Load(filepath.Join(stage, filepath.FromSlash(entryTarget)), pipeline.Options{Root: stage})
	if err != nil {
		return nil, fmt.Errorf("package %s@%s is not a valid pipeline: %w", name, version, err)
	}
	if p.Name != name {
		return nil, fmt.Errorf("package %s@%s defines pipeline %q", name, version, p.Name)
	}
	if err := runtime.ValidatePipeline(&stageCfg, p); err != nil {
		return nil, fmt.Errorf("package %s@%s cannot run in this project: %w", name, version, err)
	}

	entryPath := filepath.Join(root, filepath.FromSlash(entryTarget))
	if existingFile, _, err := findPipeline(cfg, name); err == nil && !strings.EqualFold(filepath.Clean(existingFile), entryPath) {
		return nil, fmt.Errorf("pipeline %q already exists in %s; remove or rename it first", name, mustRel(root, existingFile))
	}

	lock, err := ReadLock(root)
	if err != nil {
		return nil, err
	}
	installed := map[string]string{}
	if prev, ok := lock.Packages[name]; ok {
		for _, f := range prev.Files {
			installed[f.Path] = f.SHA256
		}
	}
	result := &InstallResult{Manifest: m, Written: []string{}, Skipped: []string{}}
	var conflicts []string
	for _, rel := range sortedKeys(targets) {
		target := targets[rel]
		current, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(target)))
		switch {
		case errors.Is(err, os.ErrNotExist):
			result.Written = append(result.Written, target)
		case err != nil:
			return nil, err
		case hashBytes(current) == hashBytes(contents[rel]):
			result.Skipped = append(result.Skipped, target)
		case installed[target] == hashBytes(current) || opts.Force:
			// Files unchanged since a previous install upgrade freely.
			result.Replaced = append(result.Replaced, target)
		default:
			conflicts = append(conflicts, target)
		}
	}
	if len(conflicts) > 0 {
		return nil, fmt.Errorf("these files exist and differ from %s@%s: %s (pass --force to overwrite)", name, version, strings.Join(conflicts, ", "))
	}
	var lockFiles []File
	for _, rel := range sortedKeys(targets) {
		target := targets[rel]
		dst := filepath.Join(root, filepath.FromSlash(target))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return nil, err
		}
		if err := writeAtomic(dst, contents[rel]); err != nil {
			return nil, err
		}
		lockFiles = append(lockFiles, File{Path: target, SHA256: hashBytes(contents[rel]), Size: int64(len(contents[rel]))})
	}
	lock.Packages[name] = LockEntry{
		Version:     version,
		Digest:      m.Digest,
		Registry:    r.Dir,
		InstalledAt: time.Now().UTC().Truncate(time.Second),
		Files:       lockFiles,
	}
	if err := WriteLock(root, lock); err != nil {
		return nil, err
	}
	return result, nil
}

// mapPath moves files under the publisher's pipelines directory into the
// consumer's; other files keep their project-relative path.
func mapPath(rel, from, to string) string {
	from, to = path.Clean(from), path.Clean(to)
	if from == to || from == "." {
		return rel
	}
	if strings.HasPrefix(rel, from+"/") {
		return path.Join(to, strings.TrimPrefix(rel, from+"/"))
	}
	return rel
}

// LockEntry is one installed package.
type LockEntry struct {
	Version     string    `json:"version"`
	Digest      string    `json:"digest"`
	Registry    string    `json:"registry,omitempty"`
	InstalledAt time.Time `json:"installed_at"`
	Files       []File    `json:"files"`
}

// Lock is the project's record of installed packages.
type Lock struct {
	Packages map[string]LockEntry `json:"packages"`
}

// ReadLock reads the project lock file; a missing file is an empty lock.
func ReadLock(root string) (*Lock, error) {
	lock := &Lock{Packages: map[string]LockEntry{}}
	data, err := os.ReadFile(filepath.Join(root, LockFile))
	if errors.Is(err, os.ErrNotExist) {
		return lock, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, lock); err != nil {
		return nil, fmt.Errorf("%s: %w", LockFile, err)
	}
	if lock.Packages == nil {
		lock.Packages = map[string]LockEntry{}
	}
	return lock, nil
}

// WriteLock saves the project lock file.
func WriteLock(root string, lock *Lock) error {
	data, err := json.MarshalIndent(lock, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(filepath.Join(root, LockFile), append(data, '\n'))
}

func writeAtomic(dst string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".signalbox-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Rename(name, dst); err != nil {
		os.Remove(name)
		return err
	}
	return nil
}

// Publishable lists project pipelines that can be published, by name.
func Publishable(cfg *config.Config) ([]string, error) {
	pipelines, errs := pipeline.LoadDir(cfg.PipelinesPath(), pipeline.Options{Root: cfg.Root, AllowExternal: cfg.AllowExternalImports})
	if len(errs) > 0 && len(pipelines) == 0 {
		return nil, errs[0]
	}
	var names []string
	for _, name := range pipeline.Names(pipelines) {
		if ValidName(name) {
			names = append(names, name)
		}
	}
	return names, nil
}

// findPipeline returns the file and parsed pipeline with the given name in
// the project's pipelines directory.
func findPipeline(cfg *config.Config, name string) (string, *pipeline.Pipeline, error) {
	dir := cfg.PipelinesPath()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", nil, err
	}
	opts := pipeline.Options{Root: cfg.Root, AllowExternal: cfg.AllowExternalImports}
	var loadErrs []string
	for _, e := range entries {
		ext := strings.ToLower(filepath.Ext(e.Name()))
		if e.IsDir() || (ext != ".yaml" && ext != ".yml") {
			continue
		}
		file, err := filepath.Abs(filepath.Join(dir, e.Name()))
		if err != nil {
			return "", nil, err
		}
		p, err := pipeline.Load(file, opts)
		if err != nil {
			loadErrs = append(loadErrs, e.Name()+": "+err.Error())
			continue
		}
		if p.Name == name {
			return file, p, nil
		}
	}
	if len(loadErrs) > 0 {
		return "", nil, fmt.Errorf("pipeline %q not found in %s (files that failed to load: %s)", name, dir, strings.Join(loadErrs, "; "))
	}
	return "", nil, fmt.Errorf("pipeline %q not found in %s", name, dir)
}

func mustRel(root, file string) string {
	rel, err := filepath.Rel(root, file)
	if err != nil {
		return file
	}
	return rel
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
