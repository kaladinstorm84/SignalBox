package engine

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kaladinstorm84/SignalBox/internal/config"
	"github.com/kaladinstorm84/SignalBox/internal/pipeline"
	sbruntime "github.com/kaladinstorm84/SignalBox/internal/runtime"
	"github.com/kaladinstorm84/SignalBox/internal/schema"
)

const invoiceSchema = `{"type":"object","required":["invoice","total"],"properties":{"invoice":{"type":"string"},"total":{"type":"number"}}}`

// requestLog records the requests each mock runtime received.
type requestLog struct {
	mu   sync.Mutex
	reqs map[string][]sbruntime.Request
	// seen records, per runtime, the file names present in FilesDir at call time.
	seen map[string][]string
}

func (l *requestLog) handler(name string) func(context.Context, sbruntime.Request) (string, error) {
	return func(_ context.Context, req sbruntime.Request) (string, error) {
		l.mu.Lock()
		defer l.mu.Unlock()
		if l.reqs == nil {
			l.reqs = map[string][]sbruntime.Request{}
			l.seen = map[string][]string{}
		}
		l.reqs[name] = append(l.reqs[name], req)
		if req.FilesDir != "" {
			entries, _ := os.ReadDir(req.FilesDir)
			for _, e := range entries {
				l.seen[name] = append(l.seen[name], e.Name())
			}
		}
		return name + " output", nil
	}
}

func (l *requestLog) last(name string) sbruntime.Request {
	l.mu.Lock()
	defer l.mu.Unlock()
	reqs := l.reqs[name]
	if len(reqs) == 0 {
		return sbruntime.Request{}
	}
	return reqs[len(reqs)-1]
}

func newUploadEngine(t *testing.T, log *requestLog, names []string, yamls ...string) (*Engine, *Store) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "invoice.json"), []byte(invoiceSchema), 0o600); err != nil {
		t.Fatal(err)
	}
	set, errs := schema.LoadDir(dir)
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	pipelines := map[string]*pipeline.Pipeline{}
	for _, y := range yamls {
		p, err := pipeline.ParseWith([]byte(y), pipeline.Options{Schemas: set})
		if err != nil {
			t.Fatalf("pipeline: %v", err)
		}
		pipelines[p.Name] = p
	}
	registry, err := sbruntime.NewRegistry(map[string]config.RuntimeConfig{"mock": {Type: "mock"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		registry.Register(&sbruntime.Mock{RuntimeName: name, Handler: log.handler(name)})
	}
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	eng, err := New(pipelines, registry, store)
	if err != nil {
		t.Fatal(err)
	}
	return eng, store
}

const uploadPipeline = `
name: up
files:
  required: true
  max_count: 3
  extensions: [csv, .txt, .json]
  schema: invoice
params:
  main:
    type: file
    required: false
agents:
  - id: inline
    runtime: inl
    prompt: read ${{ params.main }}
    files: inline
    files_budget: 30
    handoff: [path]
  - id: path
    runtime: pth
    prompt: by path
    files: path
    handoff: [plain]
  - id: plain
    runtime: pln
    prompt: nothing
`

func TestUploadsValidation(t *testing.T) {
	log := &requestLog{}
	eng, _ := newUploadEngine(t, log, []string{"inl", "pth", "pln"}, uploadPipeline)
	csv := FileInput{Name: "a.csv", Data: []byte("x,y\n1,2\n")}
	cases := []struct {
		name string
		opts StartOptions
		want string
	}{
		{"required", StartOptions{Pipeline: "up", Input: "go"}, "requires at least one file"},
		{"max count", StartOptions{Pipeline: "up", Files: []FileInput{csv, csv, csv, csv}}, "at most 3"},
		{"pipeline ext", StartOptions{Pipeline: "up", Files: []FileInput{{Name: "a.md", Data: []byte("x")}}}, "accepts only"},
		{"project ext", StartOptions{Pipeline: "up", Files: []FileInput{{Name: "a.exe", Data: []byte("x")}}}, "not allowed"},
		{"no ext", StartOptions{Pipeline: "up", Files: []FileInput{{Name: "README", Data: []byte("x")}}}, "no extension"},
		{"schema", StartOptions{Pipeline: "up", Files: []FileInput{{Name: "i.json", Data: []byte(`{"invoice":"x"}`)}}}, "does not match schema invoice"},
		{"bad json", StartOptions{Pipeline: "up", Files: []FileInput{{Name: "i.json", Data: []byte(`{`)}}}, "does not match schema invoice"},
		{"param names missing file", StartOptions{Pipeline: "up", Files: []FileInput{csv}, Params: map[string]string{"main": "other.csv"}}, "other.csv"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := eng.StartWith(tc.opts)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
	if items := eng.List(); len(items) != 0 {
		t.Fatalf("rejected uploads created %d executions", len(items))
	}

	eng.SetUploadLimits(UploadLimits{MaxBytes: 4, MaxFiles: 10, AllowedExtensions: []string{".csv"}})
	if _, err := eng.StartWith(StartOptions{Pipeline: "up", Files: []FileInput{csv}}); err == nil || !strings.Contains(err.Error(), "uploads.max_bytes") {
		t.Fatalf("size limit err = %v", err)
	}
}

func TestUploadsDelivery(t *testing.T) {
	log := &requestLog{}
	eng, store := newUploadEngine(t, log, []string{"inl", "pth", "pln"}, uploadPipeline)
	long := strings.Repeat("z", 50)
	id, err := eng.StartWith(StartOptions{Pipeline: "up", Input: "go", Params: map[string]string{"main": "../Data File.csv"}, Files: []FileInput{
		{Name: "../Data File.csv", Data: []byte("a,b\n")},
		{Name: "notes.txt", Data: []byte(long)},
		{Name: "inv.json", Data: []byte(`{"invoice":"INV-1","total":3}`)},
	}})
	if err != nil {
		t.Fatal(err)
	}
	x := waitDone(t, eng, id)
	if x.Status != StatusCompleted {
		t.Fatalf("status %s: %s", x.Status, x.Error)
	}
	if len(x.Files) != 3 || x.Files[0].Name != "Data File.csv" || x.Files[2].Schema != "invoice" || x.Files[1].Schema != "" {
		t.Fatalf("files = %+v", x.Files)
	}
	if x.Params["main"] != "Data File.csv" {
		t.Fatalf("file param = %q, want the stored name", x.Params["main"])
	}

	inl := log.last("inl")
	if !strings.Contains(inl.SystemPrompt, "read Data File.csv") {
		t.Fatalf("prompt = %q", inl.SystemPrompt)
	}
	if !strings.Contains(inl.Input, "--- file: Data File.csv ---\na,b") || !strings.Contains(inl.Input, "[truncated:") || inl.FilesDir != "" {
		t.Fatalf("inline input = %q", inl.Input)
	}
	runs := map[string]*NodeRun{}
	for _, r := range x.Runs {
		runs[r.NodeID] = r
	}
	if f := runs["inline"].Files; f == nil || f.Mode != pipeline.FilesInline || f.Budget != 30 || len(f.Truncated) != 1 || len(f.Omitted) != 1 {
		t.Fatalf("inline run files = %+v", f)
	}

	pth := log.last("pth")
	if pth.FilesDir == "" || len(pth.Files) != 3 || strings.Contains(pth.Input, "attached files") {
		t.Fatalf("path request = %+v", pth)
	}
	if got := strings.Join(log.seen["pth"], ","); got != "Data File.csv,inv.json,notes.txt" {
		t.Fatalf("files dir held %s", got)
	}
	stored := filepath.Join(store.UploadsDir(), id, "files")
	if strings.HasPrefix(pth.FilesDir, stored) {
		t.Fatalf("path delivery exposes the stored originals: %s", pth.FilesDir)
	}
	if runs["path"].Files == nil || runs["path"].Files.Mode != pipeline.FilesPath {
		t.Fatalf("path run files = %+v", runs["path"].Files)
	}

	pln := log.last("pln")
	if pln.FilesDir != "" || len(pln.Files) != 0 || strings.Contains(pln.Input, "attached files") || runs["plain"].Files != nil {
		t.Fatalf("plain node received files: %+v", pln)
	}

	path, meta, err := eng.UploadedFile(id, "inv.json")
	if err != nil || meta.Size != int64(len(`{"invoice":"INV-1","total":3}`)) {
		t.Fatalf("UploadedFile = %v %+v", err, meta)
	}
	if !strings.HasPrefix(path, stored) {
		t.Fatalf("stored path %s outside %s", path, stored)
	}
	for _, name := range []string{"../inv.json", "missing.csv", "files"} {
		if _, _, err := eng.UploadedFile(id, name); err == nil {
			t.Fatalf("UploadedFile(%q) succeeded", name)
		}
	}

	if err := eng.Delete(id); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(store.UploadsDir(), id)); !os.IsNotExist(err) {
		t.Fatalf("upload dir survived delete: %v", err)
	}
}

func TestUploadsReuseOnRerunAndRestart(t *testing.T) {
	log := &requestLog{}
	eng, _ := newUploadEngine(t, log, []string{"inl", "pth", "pln"}, uploadPipeline)
	id, err := eng.StartWith(StartOptions{Pipeline: "up", Files: []FileInput{{Name: "a.csv", Data: []byte("1,2\n")}}})
	if err != nil {
		t.Fatal(err)
	}
	src := waitDone(t, eng, id)

	rerun, err := eng.StartWith(StartOptions{Pipeline: "up", FilesFrom: id})
	if err != nil {
		t.Fatal(err)
	}
	restart, err := eng.Restart(id, RestartOptions{NodeID: "path"})
	if err != nil {
		t.Fatal(err)
	}
	for _, newID := range []string{rerun, restart} {
		x := waitDone(t, eng, newID)
		if x.Status != StatusCompleted || len(x.Files) != 1 || x.Files[0].SHA256 != src.Files[0].SHA256 || x.Files[0].ReusedFrom != id {
			t.Fatalf("%s files = %+v (%s)", newID, x.Files, x.Error)
		}
		if _, _, err := eng.UploadedFile(newID, "a.csv"); err != nil {
			t.Fatal(err)
		}
	}
	// Deleting the source must not remove the copies the others use.
	if err := eng.Delete(id); err != nil {
		t.Fatal(err)
	}
	path, _, err := eng.UploadedFile(rerun, "a.csv")
	if err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != "1,2\n" {
		t.Fatalf("rerun file after source delete = %q %v", data, err)
	}
}

func TestUploadsInheritedByChild(t *testing.T) {
	log := &requestLog{}
	parent := `
name: parent
agents:
  - id: inherit
    type: pipeline
    pipeline: up
    files: inherit
    handoff: [without]
  - id: without
    type: pipeline
    pipeline: up
`
	eng, _ := newUploadEngine(t, log, []string{"inl", "pth", "pln"}, uploadPipeline, parent)
	id, err := eng.StartWith(StartOptions{Pipeline: "parent", Input: "go", Files: []FileInput{{Name: "a.csv", Data: []byte("1,2\n")}}})
	if err != nil {
		t.Fatal(err)
	}
	// The second child does not inherit, so up's files.required fails it.
	failed := waitForRetry(t, eng, id)
	if failed.NodeID != "without" || !strings.Contains(failed.Error, "requires at least one file") {
		t.Fatalf("failed run = %s: %q", failed.NodeID, failed.Error)
	}
	if err := eng.ResolveFailure(id, failed.ID, false); err != nil {
		t.Fatal(err)
	}
	x := waitDone(t, eng, id)
	var child string
	for _, r := range x.Runs {
		if r.NodeID == "inherit" {
			if r.Status != StatusCompleted || r.Files == nil || r.Files.Mode != pipeline.FilesInherit || len(r.ChildExecutions) != 1 {
				t.Fatalf("inherit run = %+v", r)
			}
			child = r.ChildExecutions[0]
		}
	}
	cx, err := eng.Get(child)
	if err != nil || len(cx.Files) != 1 || cx.Files[0].ReusedFrom != id {
		t.Fatalf("child files = %+v %v", cx, err)
	}
	if !strings.Contains(log.last("inl").Input, "--- file: a.csv ---") {
		t.Fatal("child agent did not receive the inherited file")
	}
}

func TestUploadsPruneAndSweep(t *testing.T) {
	dir := t.TempDir()
	stray := filepath.Join(dir, "uploads", "gone", "files")
	if err := os.MkdirAll(stray, 0o755); err != nil {
		t.Fatal(err)
	}
	old := filepath.Join(dir, "uploads", "gone")
	past := time.Now().Add(-time.Hour)
	if err := os.Chtimes(old, past, past); err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(dir, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatalf("orphan upload dir survived the sweep: %v", err)
	}
	for i, id := range []string{"exec-1", "exec-2"} {
		if err := os.MkdirAll(filepath.Join(store.UploadsDir(), id, "files"), 0o755); err != nil {
			t.Fatal(err)
		}
		e := &Execution{ID: id, Pipeline: "p", Status: StatusCompleted, CreatedAt: past.Add(time.Duration(i) * time.Second)}
		if err := store.Save(e); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(filepath.Join(store.UploadsDir(), "exec-1")); !os.IsNotExist(err) {
		t.Fatalf("pruned execution kept its uploads: %v", err)
	}
	if _, err := os.Stat(filepath.Join(store.UploadsDir(), "exec-2")); err != nil {
		t.Fatalf("retained execution lost its uploads: %v", err)
	}
}

func TestUploadsCodeNodeReadsFilesDir(t *testing.T) {
	if _, err := exec.LookPath("python"); err != nil {
		t.Skip("Python is required for this integration test")
	}
	root := t.TempDir()
	script := "import os\nd = os.environ['SIGNALBOX_FILES_DIR']\nfor n in sorted(os.listdir(d)):\n    print(n, open(os.path.join(d, n)).read().strip())\n"
	if err := os.WriteFile(filepath.Join(root, "read.py"), []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	eng := newSchemaEngine(t, "name: p\nentry: c\nagents:\n  - id: c\n    type: code\n    language: python\n    code-source: read.py\n    files: path\n", root, nil)
	eng.filesRoot = t.TempDir()
	id, err := eng.StartWith(StartOptions{Pipeline: "p", Files: []FileInput{{Name: "b.txt", Data: []byte("bee")}, {Name: "a.csv", Data: []byte("1,2")}}})
	if err != nil {
		t.Fatal(err)
	}
	x := waitDone(t, eng, id)
	if out := strings.TrimSpace(runByNode(x, "c").Output); x.Status != StatusCompleted || strings.ReplaceAll(out, "\r\n", "\n") != "a.csv 1,2\nb.txt bee" {
		t.Fatalf("status %s output %q (%s)", x.Status, out, x.Error)
	}
}

func TestSanitizeFileName(t *testing.T) {
	cases := map[string]string{
		"report.csv":          "report.csv",
		"../../etc/passwd":    "passwd",
		`..\..\win.ini`:       "win.ini",
		"a b/c d.txt":         "c d.txt",
		"CON.txt":             "_CON.txt",
		"nul":                 "_nul",
		"...":                 "file",
		"":                    "file",
		"résumé.md":           "résumé.md",
		"bad\x00name\n.txt":   "badname.txt",
		" trailing. ":         "trailing",
		"semi;colon\"q'.json": "semi_colon_q_.json",
	}
	for in, want := range cases {
		if got := SanitizeFileName(in); got != want {
			t.Errorf("SanitizeFileName(%q) = %q, want %q", in, got, want)
		}
	}
	if got := SanitizeFileName(strings.Repeat("a", 300) + ".csv"); len(got) > 120 || !strings.HasSuffix(got, ".csv") {
		t.Errorf("long name = %q (%d bytes)", got, len(got))
	}
}
