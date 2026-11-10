# File uploads

[Back to SignalBox](../README.md)

### File uploads

An execution can carry files: attach them in the New run form (drop zone or
browse), with `signalbox run|start --file path` (repeatable), or as
`multipart/form-data` on `POST /api/executions`. Files are validated before
the execution is created, so a bad upload never starts a run, and are stored
read-only under `<state_dir>/uploads/<execution>/files/`.

A pipeline can declare what it accepts; all keys are optional:

```yaml
files:
  required: true            # refuse runs without files
  max_count: 3              # at most this many (default: uploads.max_files)
  extensions: [.csv, .md]   # narrower than uploads.allowed_extensions
  schema: invoice           # .json/.yaml/.yml uploads must match schemas/invoice.json
params:
  ledger:
    type: file              # a file picker in the UI; value = the attached file's name
```

Nodes never see the files unless they ask, so existing pipelines are
unchanged. Set `files:` per node:

| `files:` | Nodes | Delivery |
|----------|-------|----------|
| `inline` | standard, code, llm_gate | Text files are appended to the input between `--- file: name ---` markers, up to `files_budget` characters (default 20,000). Longer files are truncated with a note; binary files and files past the budget are listed by name only. Works with every runtime, including `openai`. |
| `path` | standard, code, llm_gate | Read-only copies are placed in a per-run folder named by `SIGNALBOX_FILES_DIR`; agents get the folder and file list in their prompt, and `claude`/`copilot` also get `--add-dir`. Code steps read the folder from the environment. Not available on `openai` runtimes. |
| `inherit` | pipeline | The linked child execution receives this execution's files (hard-linked, not copied). Without it the child starts with none. |

Each run records which files it received, how, and whether any were
truncated; the execution page lists the files with size, type, schema check
and download links, and `signalbox show` prints them. **Rerun**, **Restart**
and inherited child executions reuse the original files (marked `reused`), and
deleting or pruning an execution removes its upload folder. Downloads are
always served as attachments with `nosniff` and a sandbox CSP, and only names
recorded on the execution resolve. Watchers cannot attach files, so a watched
pipeline may not set `files.required`.

Project limits live in `signalbox.yaml` (see below): `uploads.max_bytes` per
file (default 10 MiB), `uploads.max_files` per execution (default 10) and
`uploads.allowed_extensions` (defaults to common text, data, office and image
types). Names are reduced to a safe base name (paths, control characters and
Windows device names are stripped or escaped) and made unique per execution.
When running in Docker, uploads live in the state volume with the database.

Demos (sample files in `testproject/samples/`):

- `upload-review` — an agent reads files inline, `libs/file_stats.py` reads
  them by path, and a gate checks the result against the same files:
  `signalbox run upload-review --file samples/orders.csv --file samples/notes.md`
- `upload-invoice` — schema-validated JSON upload plus a `type: file`
  parameter: `--file samples/invoice-1042.json --param invoice=invoice-1042.json`
  (try `samples/invoice-bad.json` to see it rejected before the run starts)
- `upload-chained` — a pipeline node passes the files to `upload-review` with
  `files: inherit`.
- `manuscript-review` — editorial advice on one `.txt`, `.md` or `.docx`
  manuscript. `libs/manuscript_profile.py` builds a chapter map and prose
  statistics. Structure, character, prose, continuity and market editors then
  read the full text by path in parallel and return schema-checked notes
  (`schemas/editorial-notes.json`). A senior editor writes a prioritised
  editorial letter, a gate checks it against the manuscript, and a human
  editor signs it off or sends it back. This demo needs a CLI runtime because
  it uses `files: path`:
  `signalbox run manuscript-review --file samples/manuscript-sample.md --param genre="literary mystery" --input "Worried the middle drags."`
