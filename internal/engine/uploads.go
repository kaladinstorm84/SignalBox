package engine

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/kaladinstorm84/SignalBox/internal/config"
	"github.com/kaladinstorm84/SignalBox/internal/pipeline"
	"github.com/kaladinstorm84/SignalBox/internal/runtime"
	"github.com/kaladinstorm84/SignalBox/internal/schema"
	"gopkg.in/yaml.v3"
)

// UploadedFile is the metadata of one file attached to an execution. The
// content lives under state_dir/uploads/<execution>/files/<name> and is
// never executed.
type UploadedFile struct {
	Name        string `json:"name"`
	Size        int64  `json:"size"`
	ContentType string `json:"content_type"`
	SHA256      string `json:"sha256"`
	// Text marks UTF-8 text that inline delivery can include.
	Text bool `json:"text,omitempty"`
	// Schema names the schema the file was validated against on upload.
	Schema string `json:"schema,omitempty"`
	// ReusedFrom names the execution the file was carried over from by a
	// rerun, restart, or files: inherit.
	ReusedFrom string `json:"reused_from,omitempty"`
}

// RunFiles records the files delivered to one run.
type RunFiles struct {
	Mode  string   `json:"mode"`
	Names []string `json:"names"`
	// Dir is the per-run folder holding copies (files: path).
	Dir string `json:"dir,omitempty"`
	// Chars counts inlined characters; Truncated and Omitted list files
	// cut short by the budget or listed without content (binary files, or
	// once the budget is spent).
	Chars     int      `json:"chars,omitempty"`
	Budget    int      `json:"budget,omitempty"`
	Truncated []string `json:"truncated,omitempty"`
	Omitted   []string `json:"omitted,omitempty"`
}

// FileInput is one file supplied when starting an execution.
type FileInput struct {
	Name string
	Data []byte
}

// UploadLimits bounds the files attached to one execution.
type UploadLimits struct {
	MaxBytes          int64
	MaxFiles          int
	AllowedExtensions []string
}

// DefaultUploadLimits returns the limits used when none are configured.
func DefaultUploadLimits() UploadLimits {
	return UploadLimits{
		MaxBytes:          config.DefaultUploadMaxBytes,
		MaxFiles:          config.DefaultUploadMaxFiles,
		AllowedExtensions: append([]string(nil), config.DefaultUploadExtensions...),
	}
}

// UploadLimitsFromConfig converts the project's uploads settings.
func UploadLimitsFromConfig(c config.UploadsConfig) UploadLimits {
	limits := DefaultUploadLimits()
	if c.MaxBytes > 0 {
		limits.MaxBytes = c.MaxBytes
	}
	if c.MaxFiles > 0 {
		limits.MaxFiles = c.MaxFiles
	}
	if len(c.AllowedExtensions) > 0 {
		limits.AllowedExtensions = append([]string(nil), c.AllowedExtensions...)
	}
	return limits
}

// SetUploadLimits replaces the limits applied to new executions.
func (eng *Engine) SetUploadLimits(limits UploadLimits) {
	eng.mu.Lock()
	defer eng.mu.Unlock()
	eng.uploadLimits = limits
}

// UploadLimits returns the limits applied to new executions.
func (eng *Engine) UploadLimits() UploadLimits {
	eng.mu.Lock()
	defer eng.mu.Unlock()
	return eng.uploadLimits
}

// StartOptions describes a new top-level execution.
type StartOptions struct {
	Pipeline string
	Input    string
	Params   map[string]string
	// Files are new uploads.
	Files []FileInput
	// FilesFrom reuses the files of an existing execution (rerun).
	FilesFrom string
}

// StartWith begins a new execution with parameters and files. Files and
// parameters are validated before anything runs.
func (eng *Engine) StartWith(opts StartOptions) (string, error) {
	eng.mu.Lock()
	defer eng.mu.Unlock()
	spec := startSpec{pipeline: opts.Pipeline, input: opts.Input, params: opts.Params, files: opts.Files}
	if opts.FilesFrom != "" {
		source, err := eng.loadCopyLocked(opts.FilesFrom)
		if err != nil {
			return "", err
		}
		spec.reuse = &reusedFiles{exec: source.ID, files: source.Files}
	}
	le, err := eng.startLocked(spec)
	if err != nil {
		return "", err
	}
	return le.exec.ID, nil
}

// reusedFiles are files carried over from another execution.
type reusedFiles struct {
	exec  string
	files []UploadedFile
}

// fileSource is a validated file waiting to be stored: new data, or an
// existing file to link.
type fileSource struct {
	meta UploadedFile
	data []byte
	src  string
	// original is the name the client supplied.
	original string
}

// uploadsRoot is the absolute directory holding every execution's files.
func (eng *Engine) uploadsRoot() (string, error) {
	if eng.filesRoot != "" {
		return eng.filesRoot, nil
	}
	var root string
	if eng.store != nil {
		root = eng.store.UploadsDir()
	} else {
		dir, err := os.MkdirTemp("", "signalbox-uploads-")
		if err != nil {
			return "", err
		}
		root = dir
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	eng.filesRoot = abs
	return abs, nil
}

func execFilesDir(root, execID string) string {
	return filepath.Join(root, execID, "files")
}

// collectFilesLocked sanitises and validates the files for a new execution
// against the project limits and the pipeline's files declaration.
func (eng *Engine) collectFilesLocked(p *pipeline.Pipeline, spec startSpec) ([]fileSource, error) {
	var sources []fileSource
	used := map[string]bool{}
	if spec.reuse != nil && len(spec.reuse.files) > 0 {
		root, err := eng.uploadsRoot()
		if err != nil {
			return nil, err
		}
		for _, f := range spec.reuse.files {
			src := filepath.Join(execFilesDir(root, spec.reuse.exec), f.Name)
			if _, err := os.Stat(src); err != nil {
				return nil, fmt.Errorf("file %q of execution %s is no longer available", f.Name, spec.reuse.exec)
			}
			meta := f
			meta.ReusedFrom = spec.reuse.exec
			meta.Schema = ""
			used[strings.ToLower(f.Name)] = true
			sources = append(sources, fileSource{meta: meta, src: src, original: f.Name})
		}
	}
	for _, in := range spec.files {
		name := uniqueName(SanitizeFileName(in.Name), used)
		sum := sha256.Sum256(in.Data)
		sources = append(sources, fileSource{
			meta: UploadedFile{
				Name:        name,
				Size:        int64(len(in.Data)),
				ContentType: contentType(name, in.Data),
				SHA256:      hex.EncodeToString(sum[:]),
				Text:        isText(in.Data),
			},
			data:     in.Data,
			original: in.Name,
		})
	}
	if err := eng.checkFiles(p, sources); err != nil {
		return nil, err
	}
	return sources, nil
}

func (eng *Engine) checkFiles(p *pipeline.Pipeline, sources []fileSource) error {
	limits := eng.uploadLimits
	decl := p.Files
	if decl != nil && decl.Required && len(sources) == 0 {
		return fmt.Errorf("pipeline %q requires at least one file", p.Name)
	}
	if len(sources) > limits.MaxFiles {
		return fmt.Errorf("%d files attached; at most %d are allowed per execution (uploads.max_files)", len(sources), limits.MaxFiles)
	}
	if decl != nil && decl.MaxCount > 0 && len(sources) > decl.MaxCount {
		return fmt.Errorf("pipeline %q accepts at most %d file(s); %d attached", p.Name, decl.MaxCount, len(sources))
	}
	for i := range sources {
		f := &sources[i].meta
		ext := strings.ToLower(filepath.Ext(f.Name))
		if ext == "" {
			return fmt.Errorf("file %q has no extension; uploads must have an allowed extension", f.Name)
		}
		if !containsString(limits.AllowedExtensions, ext) {
			return fmt.Errorf("file %q: %s files are not allowed (uploads.allowed_extensions: %s)", f.Name, ext, strings.Join(limits.AllowedExtensions, " "))
		}
		if decl != nil && len(decl.Extensions) > 0 && !containsString(decl.Extensions, ext) {
			return fmt.Errorf("file %q: pipeline %q accepts only %s", f.Name, p.Name, strings.Join(decl.Extensions, " "))
		}
		if f.Size > limits.MaxBytes {
			return fmt.Errorf("file %q is %d bytes; the limit is %d (uploads.max_bytes)", f.Name, f.Size, limits.MaxBytes)
		}
		if decl == nil || decl.Schema == "" || !structuredExt(ext) {
			continue
		}
		sch := p.SchemaFor(decl.Schema)
		if sch == nil {
			return fmt.Errorf("pipeline %q: schema %q is not loaded", p.Name, decl.Schema)
		}
		data := sources[i].data
		if sources[i].src != "" {
			var err error
			if data, err = os.ReadFile(sources[i].src); err != nil {
				return err
			}
		}
		if err := validateFileSchema(sch, ext, data); err != nil {
			return fmt.Errorf("file %q does not match schema %s: %w", f.Name, decl.Schema, err)
		}
		f.Schema = decl.Schema
	}
	return nil
}

func structuredExt(ext string) bool {
	return ext == ".json" || ext == ".yaml" || ext == ".yml"
}

func validateFileSchema(sch *schema.Schema, ext string, data []byte) error {
	text := string(data)
	if ext != ".json" {
		var doc any
		if err := yaml.Unmarshal(data, &doc); err != nil {
			return fmt.Errorf("invalid YAML: %w", err)
		}
		converted, err := json.Marshal(doc)
		if err != nil {
			return fmt.Errorf("YAML cannot be represented as JSON: %w", err)
		}
		text = string(converted)
	}
	v, err := schema.Parse(text)
	if err != nil {
		return fmt.Errorf("invalid JSON: %w", err)
	}
	if errs := sch.Validate(v); len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return nil
}

// bindFileParams maps file parameter values (as the client named the file)
// to stored names, and rejects values that name no attached file.
func bindFileParams(p *pipeline.Pipeline, params map[string]string, sources []fileSource) (map[string]string, error) {
	fileParams := p.FileParams()
	if len(fileParams) == 0 {
		return params, nil
	}
	bound := make(map[string]string, len(params))
	for k, v := range params {
		bound[k] = v
	}
	for _, name := range fileParams {
		value, ok := bound[name]
		if !ok || value == "" {
			continue
		}
		found := ""
		for _, s := range sources {
			if s.meta.Name == value || s.original == value {
				found = s.meta.Name
				break
			}
		}
		if found == "" {
			return nil, fmt.Errorf("param %q names file %q, which is not attached", name, value)
		}
		bound[name] = found
	}
	return bound, nil
}

// storeFilesLocked writes the files into the execution's upload folder.
// Identical content is stored once (hard-linked) where the filesystem
// allows.
func (eng *Engine) storeFilesLocked(execID string, sources []fileSource) ([]UploadedFile, error) {
	if len(sources) == 0 {
		return nil, nil
	}
	root, err := eng.uploadsRoot()
	if err != nil {
		return nil, err
	}
	dir := execFilesDir(root, execID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("creating upload folder: %w", err)
	}
	byHash := map[string]string{}
	files := make([]UploadedFile, 0, len(sources))
	for _, s := range sources {
		dst := filepath.Join(dir, s.meta.Name)
		src := s.src
		if prior, ok := byHash[s.meta.SHA256]; ok {
			src = prior
		}
		if src != "" {
			err = linkOrCopy(src, dst)
		} else {
			err = os.WriteFile(dst, s.data, 0o600)
		}
		if err != nil {
			_ = os.RemoveAll(filepath.Join(root, execID))
			return nil, fmt.Errorf("storing file %q: %w", s.meta.Name, err)
		}
		byHash[s.meta.SHA256] = dst
		files = append(files, s.meta)
	}
	return files, nil
}

// reuseFilesLocked links another execution's files into a new one.
func (eng *Engine) reuseFilesLocked(sourceExec string, files []UploadedFile, execID string) ([]UploadedFile, error) {
	if len(files) == 0 {
		return nil, nil
	}
	root, err := eng.uploadsRoot()
	if err != nil {
		return nil, err
	}
	sources := make([]fileSource, 0, len(files))
	for _, f := range files {
		meta := f
		meta.ReusedFrom = sourceExec
		sources = append(sources, fileSource{meta: meta, src: filepath.Join(execFilesDir(root, sourceExec), f.Name)})
	}
	return eng.storeFilesLocked(execID, sources)
}

// removeUploads deletes an execution's upload folder.
func (eng *Engine) removeUploadsLocked(execID string) {
	if eng.filesRoot == "" && eng.store == nil {
		return
	}
	root, err := eng.uploadsRoot()
	if err != nil {
		return
	}
	_ = os.RemoveAll(filepath.Join(root, execID))
}

// UploadedFile returns the on-disk path and metadata of one of an
// execution's files. Only names recorded on the execution resolve, so a
// request cannot reach outside its folder.
func (eng *Engine) UploadedFile(execID, name string) (string, UploadedFile, error) {
	exec, err := eng.Get(execID)
	if err != nil {
		return "", UploadedFile{}, err
	}
	for _, f := range exec.Files {
		if f.Name != name {
			continue
		}
		eng.mu.Lock()
		root, err := eng.uploadsRoot()
		eng.mu.Unlock()
		if err != nil {
			return "", UploadedFile{}, err
		}
		return filepath.Join(execFilesDir(root, execID), f.Name), f, nil
	}
	return "", UploadedFile{}, fmt.Errorf("execution %s has no file %q", execID, name)
}

// fileDelivery is what a run receives for files: inline text appended to
// the runtime input, or a folder of copies.
type fileDelivery struct {
	suffix string
	dir    string
	paths  []string
}

func (d fileDelivery) apply(req *runtime.Request) {
	req.Input += d.suffix
	req.FilesDir = d.dir
	req.Files = d.paths
}

// deliverFiles prepares the node's files and records them on the run.
func (eng *Engine) deliverFiles(le *liveExec, run *NodeRun, node *pipeline.Node) (fileDelivery, error) {
	mode := node.FilesMode()
	files := le.exec.Files
	if (mode != pipeline.FilesInline && mode != pipeline.FilesPath) || len(files) == 0 {
		return fileDelivery{}, nil
	}
	eng.mu.Lock()
	root, err := eng.uploadsRoot()
	eng.mu.Unlock()
	if err != nil {
		return fileDelivery{}, err
	}
	srcDir := execFilesDir(root, le.exec.ID)
	record := &RunFiles{Mode: mode}
	var d fileDelivery
	if mode == pipeline.FilesPath {
		d.dir = filepath.Join(root, le.exec.ID, "work", run.ID)
		if err := os.RemoveAll(d.dir); err != nil {
			return d, err
		}
		if err := os.MkdirAll(d.dir, 0o700); err != nil {
			return d, fmt.Errorf("creating run file folder: %w", err)
		}
		for _, f := range files {
			dst := filepath.Join(d.dir, f.Name)
			// Copy rather than link so an agent cannot alter the stored upload.
			if err := copyFile(filepath.Join(srcDir, f.Name), dst); err != nil {
				return d, fmt.Errorf("copying file %q for %s: %w", f.Name, node.ID, err)
			}
			d.paths = append(d.paths, dst)
			record.Names = append(record.Names, f.Name)
		}
		record.Dir = d.dir
	} else {
		budget := node.InlineBudget()
		record.Budget = budget
		var b strings.Builder
		b.WriteString("\n\n--- attached files ---")
		remaining := budget
		for _, f := range files {
			record.Names = append(record.Names, f.Name)
			if !f.Text {
				fmt.Fprintf(&b, "\n\n--- file: %s (%s, %s; binary, not inlined) ---", f.Name, f.ContentType, humanSize(f.Size))
				record.Omitted = append(record.Omitted, f.Name)
				continue
			}
			if remaining <= 0 {
				fmt.Fprintf(&b, "\n\n--- file: %s (%s; not inlined, file budget spent) ---", f.Name, humanSize(f.Size))
				record.Omitted = append(record.Omitted, f.Name)
				continue
			}
			data, err := os.ReadFile(filepath.Join(srcDir, f.Name))
			if err != nil {
				return d, fmt.Errorf("reading file %q for %s: %w", f.Name, node.ID, err)
			}
			text := string(data)
			total := utf8.RuneCountInString(text)
			fmt.Fprintf(&b, "\n\n--- file: %s ---\n", f.Name)
			if total > remaining {
				text = truncateRunes(text, remaining)
				b.WriteString(text)
				fmt.Fprintf(&b, "\n[truncated: %d of %d characters shown]", remaining, total)
				record.Truncated = append(record.Truncated, f.Name)
				record.Chars += remaining
				remaining = 0
				continue
			}
			b.WriteString(text)
			record.Chars += total
			remaining -= total
		}
		b.WriteString("\n\n--- end of attached files ---")
		d.suffix = b.String()
	}
	eng.mu.Lock()
	run.Files = record
	eng.persistLocked(le)
	eng.mu.Unlock()
	return d, nil
}

// SanitizeFileName reduces a client-supplied name to a safe base name:
// directories and control characters are dropped, unusual punctuation is
// replaced, leading dots are removed, and Windows device names are escaped.
func SanitizeFileName(name string) string {
	if i := strings.LastIndexAny(name, `/\`); i >= 0 {
		name = name[i+1:]
	}
	var b strings.Builder
	for _, r := range name {
		switch {
		case r < 0x20 || r == 0x7f || r == utf8.RuneError:
		case unicode.IsLetter(r) || unicode.IsDigit(r) || strings.ContainsRune("._- ()+", r):
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	s := strings.Trim(b.String(), ". ")
	if s == "" {
		s = "file"
	}
	ext := filepath.Ext(s)
	stem := strings.TrimSuffix(s, ext)
	if reservedDeviceName(stem) {
		stem = "_" + stem
	}
	const maxLen = 120
	if len(ext) > 20 {
		ext = ""
	}
	for len(stem)+len(ext) > maxLen {
		_, size := utf8.DecodeLastRuneInString(stem)
		stem = stem[:len(stem)-size]
	}
	if stem == "" {
		stem = "file"
	}
	return stem + ext
}

func reservedDeviceName(stem string) bool {
	upper := strings.ToUpper(strings.TrimSpace(stem))
	if i := strings.IndexByte(upper, '.'); i >= 0 {
		upper = upper[:i]
	}
	switch upper {
	case "CON", "PRN", "AUX", "NUL":
		return true
	}
	if len(upper) == 4 && (strings.HasPrefix(upper, "COM") || strings.HasPrefix(upper, "LPT")) && upper[3] >= '1' && upper[3] <= '9' {
		return true
	}
	return false
}

// uniqueName suffixes name until it differs (case-insensitively) from the
// names already used.
func uniqueName(name string, used map[string]bool) string {
	candidate := name
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	for i := 2; used[strings.ToLower(candidate)]; i++ {
		candidate = fmt.Sprintf("%s-%d%s", stem, i, ext)
	}
	used[strings.ToLower(candidate)] = true
	return candidate
}

var knownTypes = map[string]string{
	".txt": "text/plain; charset=utf-8", ".md": "text/markdown; charset=utf-8", ".log": "text/plain; charset=utf-8",
	".csv": "text/csv; charset=utf-8", ".tsv": "text/tab-separated-values; charset=utf-8",
	".json": "application/json", ".yaml": "application/yaml", ".yml": "application/yaml", ".xml": "application/xml",
	".pdf":  "application/pdf",
	".docx": "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
	".xlsx": "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
	".pptx": "application/vnd.openxmlformats-officedocument.presentationml.presentation",
	".png":  "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".gif": "image/gif", ".webp": "image/webp",
}

func contentType(name string, data []byte) string {
	if t, ok := knownTypes[strings.ToLower(filepath.Ext(name))]; ok {
		return t
	}
	if isText(data) {
		return "text/plain; charset=utf-8"
	}
	return http.DetectContentType(data)
}

func isText(data []byte) bool {
	return utf8.Valid(data) && !strings.ContainsRune(string(data), 0)
}

func humanSize(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}

func containsString(list []string, v string) bool {
	for _, item := range list {
		if item == v {
			return true
		}
	}
	return false
}

func linkOrCopy(src, dst string) error {
	if err := os.Link(src, dst); err == nil {
		return nil
	}
	return copyFile(src, dst)
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}
