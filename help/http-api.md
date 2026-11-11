# HTTP API

[Back to SignalBox](../README.md)

## HTTP API

| Method & path | Purpose |
|---------------|---------|
| `GET /api/meta` | Project name and `uploads` limits (`max_bytes`, `max_files`, `allowed_extensions`) |
| `GET /api/pipelines` | Loaded pipeline definitions, including `params`, imported `sources`, optional `registry_sources` (package, kind, installed version), `input_schema`, `files` (the upload declaration), and each agent's `model`, `mcp`, `knowledge_bases`, `output` contract (`schema`, `on_invalid`, `max_repairs`) and `files`/`files_budget` |
| `GET /api/schemas` | Project schema files: `dir`, `schemas` (`name`, `file`, `schema`, `used_by`), and load `errors` |
| `GET /api/knowledge-bases` | Project knowledge base names, document names/sizes, and optional `registry_sources` (package, kind, installed version), including sources bundled with pipelines |
| `POST /api/knowledge-bases` | Create a base: `{"name": "product-handbook"}` |
| `PUT /api/knowledge-bases/{name}/documents/{filename}` | Create or replace a UTF-8 text document: `{"content": "..."}` |
| `POST /api/knowledge-bases/{name}/documents` | Upload one or more plain-text files as multipart `file` parts |
| `DELETE /api/knowledge-bases/{name}/documents/{filename}` / `DELETE /api/knowledge-bases/{name}` | Remove a document or a base and its documents |
| `GET /api/pipeline-files` | YAML filenames and pipeline names where valid, including invalid files for repair |
| `GET /api/pipeline-files/{filename}` | Raw pipeline YAML and file version |
| `PUT /api/pipeline-files/{filename}` | Validate and create/edit YAML: `{"content": "...", "version": "..."}`; use an empty version to create a new file |
| `GET /api/settings` | Project configuration YAML and file version |
| `PUT /api/settings` | Validate and save project YAML: `{"content": "...", "version": "..."}` |
| `GET /api/executions?page=1&page_size=20` | Paginated execution summaries (newest first); page sizes are multiples of 10 from 10 to 100. Returns `executions`, `page`, `page_size`, `total`, and `total_pages`. Child executions of pipeline nodes carry `parent` (the parent execution ID). |
| `POST /api/executions` | Start a run: `{"pipeline": "default", "input": "...", "params": {"topic": "..."}}` (`params` optional; 400 if invalid). To attach files send `multipart/form-data` with `pipeline`, `input`, `params` (a JSON string) and one `file` part per file |
| `GET /api/executions/{id}` | Full execution detail. Child executions include `parent` (`execution`, `run_id`, `node_id`, `depth`) and `section` (`from`, `until`); pipeline-node runs list `child_executions`; `surfaced` lists runs waiting in live descendants (`execution`, `pipeline`, `parent_run_id`, `run`); `files` lists attached files (`name`, `size`, `content_type`, `sha256`, `schema`, `reused_from`) and each run's `files` records what it received (`mode`, `names`, `chars`, `budget`, `truncated`, `omitted`) |
| `GET /api/executions/{id}/files/{name}` | Download an attached file (always `Content-Disposition: attachment`; 404 for names not attached to the execution) |
| `POST /api/executions/{id}/approve` | Resolve a gate: `{"run_id": "...", "approved": true, "approved_by": "...", "comment": "..."}`. `run_id` may be a surfaced run of a child execution (the same applies to `manual-edit` and `retry`) |
| `POST /api/executions/{id}/retry` | Retry a failed agent call awaiting retry: `{"run_id": "..."}` |
| `POST /api/executions/{id}/restart` | Start a new execution from a run of a finished execution: `{"run_id": "...", "node_id": "...", "stage": "...", "force": false}` (all optional; with no run, the latest failed/interrupted/canceled run). Returns 201 `{"id": "..."}`; 409 `{"error", "definition_changed": true, "changes": [...]}` when the pipeline changed and `force` is not set |
| `POST /api/executions/{id}/cancel` | Cancel a running execution |
| `POST /api/executions/{id}/rerun` | Start a new execution with the same pipeline, input, params and files; returns `{"id": "..."}` (409 for child executions; restart the parent instead) |
| `DELETE /api/executions/{id}` | Delete a finished execution (409 while it is still running) |
| `GET /api/executions/{id}/chunks` | List the execution's chunks |
| `POST /api/executions/{id}/chunks` | Add a chunk: `{"title": "...", "by": "..."}`; returns the chunk (409 once the execution has finished) |
| `POST /api/executions/{id}/chunks/{chunk}/done` | Mark a chunk done: optional `{"by": "..."}` |
| `DELETE /api/executions/{id}/chunks/{chunk}` | Remove a chunk (its ID is not reused) |
| `GET /api/registry?q=...` | Registry status: `configured`, `path`, matching `packages` (with `kind`), `installed` (lock entries), `publishable` pipeline names, `publishable_plugins` folder names, and `publishable_knowledge` base names |
| `GET /api/registry/{name}` | Latest version of a package: `manifest`, `versions`, file contents, and `verify_error` if integrity fails |
| `GET /api/registry/{name}/{version}` | A specific version (`latest` allowed) |
| `POST /api/registry/install` | Install: `{"ref": "name@1.2.0", "digest": "sha256:...", "force": false}` (409 on conflicts or validation errors) |
| `POST /api/registry/publish` | Publish: `{"pipeline": "name", "version": "", "by": "..."}`, `{"plugin": "name", "version": "", "by": "..."}` or `{"knowledge_base": "name", "description": "...", "version": "", "by": "..."}` (exactly one type); returns 201 with the manifest |

Plugin nodes retain their `type: plugin:<name>` in definitions and execution
history. Pipeline node views include `plugin` metadata (name, description,
language, entry, timeout, exit codes and folder digest), alongside the effective
code fields. Registry inspection replaces binary/large file contents with
preview notices; verification still covers all bytes. Install results may
include `removed`, listing obsolete tracked plugin files deleted on upgrade.
