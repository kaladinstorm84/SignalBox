# Plan: 

1. Add richer routing, gates, and retry policies
Build on SignalBox’s existing handoffs, approval nodes, and execution engine in pipeline.go and engine.go.

### Conditional handoffs and payload templates

Extend handoffs with conditions, such as always, on_approve, and on_reject.
Allow a handoff to construct its input from values such as the current input, output, or review comment.
Start with a small, documented condition/template language; avoid arbitrary expression execution.
Keep existing handoff: [node] behavior unchanged as the default.
LLM quality gates

Add an LLM gate that evaluates a node’s result and produces an approve/reject decision plus feedback.
Reuse the existing approval/rejection lifecycle where possible, but keep automated gate decisions distinct from human approvals in the execution record and UI.
Support a bounded rejection/rework limit; fail clearly when the limit is reached.

### Runtime retry policy

Add per-node retry count and backoff for transient runtime errors.
Keep the current user-visible waiting_retry option for failures that exhaust automatic retries.
Treat runtime errors separately from quality-gate rejection: one retries a failed call; the other sends rejected output back for rework.
Acceptance: Existing pipeline YAML behaves as before. Tests cover condition selection, payload construction, gate approve/reject, retry limits, cancellation during backoff, and failure reporting.

2. Add conversational sessions and chunks
These are new execution concepts, so introduce them as explicit node/execution behavior rather than trying to model them as ordinary sequential handoffs.

### Session nodes

Define participants, turn order, maximum rounds, consensus method, and summary agent in the pipeline schema.
Record each turn and the final transcript in the execution history.
Add round limits and clear terminal outcomes for consensus reached versus no consensus.
Chunk tracking

Add persisted chunks with status and stable IDs, plus operations to create, complete, and remove them.
Allow session output to request chunk changes through a constrained, validated directive format.
Add CLI and web UI views/actions for chunk status.
Optionally let a session require all chunks to be complete before consensus succeeds.
Acceptance: Session transcripts survive reloads; rounds stop at the configured maximum; chunk changes are validated, persisted, and visible through API/UI. Session and chunk behavior should have engine-level tests before being wired into the UI.

3. Add context strategies
Extend runtime request construction so agents can select which prior work they receive. SignalBox’s additional-input feature already models explicit dependencies, but it does not provide context selection or token management.

Add strategies such as all, recent, own, and none, with a configurable context window.
Define exactly which execution data each strategy includes and how it is formatted.
Keep explicit additional-input behavior separate and unchanged.
Add measurable tests for selected context contents and truncation/window boundaries. Avoid claiming token savings until those are measured against representative pipelines.
Acceptance: The same pipeline can choose context per node, with deterministic assembly tests and no change to default inputs for existing pipelines.

4. Add integrations and pipeline reuse
These are related but separable features. Start with reusable local pipelines; add external tool integration behind explicit configuration and security controls.

### Pipeline parameters

Declare typed parameters and defaults in a pipeline.
Resolve CLI overrides and validate required values before starting an execution.
Persist resolved parameter values with the execution so history is reproducible.
Imports and agent templates

Support importing shared definitions from local files and inheriting from reusable agent templates using 'extends:' property (similar to gitlab ci approach)

Resolve imports deterministically, detect cycles and duplicate IDs, and validate the fully resolved pipeline before execution.
Keep imports within the project root by default; make any external-path behavior explicit.

#### Runtime/model improvements

Add provider adapters or model overrides only where there is a concrete supported use case; keep existing named runtime configuration working.

#### MCP tools

Add MCP configuration and runtime lifecycle management only after defining how servers, tools, credentials, and permissions are controlled.
Require explicit opt-in and document that configured tools can perform actions as the SignalBox process user.
Avoid exposing secrets in logs or persisted execution data.
Acceptance: Parameter and import resolution are deterministic and tested. MCP calls are separately permissioned and observable; they must not be enabled implicitly by loading a pipeline.

5. Expand operations, recovery, and distribution
Stage-level recovery

Snapshot enough execution context before stages to restart from a selected stage, not just retry an interrupted callable node.
Define snapshot retention and behavior when the pipeline definition has changed since the original run.
Display the restart point and source execution in the UI.
CLI parity

Add management commands for listing and inspecting executions, cancelling, approving/rejecting, retrying, and restarting.
Reuse existing engine APIs rather than implementing a separate CLI execution path.
Add chunk commands once chunk support exists.
Pipeline registry

Treat search, publish, and install as a later, optional capability.
First settle pipeline schema/version compatibility and integrity checks.
Keep registry access separate from local execution; downloaded definitions should be inspectable and validated before use.
Acceptance: Recovery tests verify that completed work is not repeated unnecessarily and that changed pipeline definitions produce a clear warning or error. CLI and web operations produce consistent results. Registry installation verifies the selected version and rejects invalid definitions.

6. - Pipeline chaining.

Allow references to sections in other pipelines

7. Structured responses
Let an agent's output be constrained to a JSON Schema stored in the project, validated by the engine, and used by later nodes.

### Schema files

Add a schemas directory (`schemas_dir` in signalbox.yaml, default `schemas/`) holding JSON Schema files in JSON or YAML, referenced by name from a node: `output: { schema: invoice }`.
Keep schema files inside the project root, with the same rules as imports. Validate every referenced schema at load time and in `signalbox validate`, reporting unknown names and invalid schemas.
Support a documented JSON Schema draft through an existing Go validator library rather than a hand-written one.

### Producing structured output

Pass the schema to runtimes through the runtime request:
- Use native support where it exists, for example OpenAI `response_format: json_schema`, or a CLI flag configured per runtime like `model_arg`.
- Otherwise, append schema instructions to the prompt.
- The mock runtime should return a sample document that matches the schema.

Apply the same schema to code node stdout. Optionally allow `input: { schema: ... }` on a pipeline so the run input is validated before the execution starts.

### Validation and repair

Parse and validate every output against its schema; the first `{...}` or fenced JSON block may be extracted from surrounding text.
On invalid output, apply `on_invalid: repair | fail` with a bounded `max_repairs`:
- `repair` re-invokes the same agent with the validation errors appended.
- Keep repairs distinct from runtime retries, which repeat a failed call, and from gate rework, which sends accepted output back for quality reasons.
- Record each attempt and its validation errors on the run.

### Using structured data downstream

Expose validated fields to handoff payload templates and conditions, for example `${{ output.total }}` and a condition on `output.risk == "high"`.
Extend the existing small template and condition language with field paths and simple comparisons only, with no arbitrary expressions.
Let LLM gates and llm_gate feedback use a schema too, so verdicts are machine-checked.

### UI, API, and distribution

- Show the schema name on nodes and run cards.
- Render validated output as formatted JSON with a collapsible raw view.
- Show the validation errors for each attempt.
- Serve schemas through `GET /api/schemas`.
- Bundle referenced schemas when publishing to the registry.

Acceptance: Pipelines without `output.schema` behave exactly as before. Tests cover schema loading and errors, valid and invalid outputs, JSON extraction, repair limits, failure reporting, field access in templates and conditions, and code node validation. Demo pipelines use the mock and code runtimes only.

8. Upload files
Allow files to be attached to an execution and made available to the agents that opt in.

### Declaring and attaching files

A pipeline declares the files it accepts, either as `files: { required, max_count, extensions }` or as params with `type: file`. Validation errors are reported before the execution starts.
Attach files in three places: the New run form (drag-and-drop), `signalbox run|start --file path` (repeatable), and multipart `POST /api/executions`.
Add project limits in signalbox.yaml (`uploads: { max_bytes, max_files, allowed_extensions }`) with safe defaults.

### Storage

Store uploads under `state_dir/uploads/<execution>/` with sanitised names (no path traversal), size, content type, and SHA-256 hash recorded on the execution.
Never execute uploads. Serve downloads as attachments with `X-Content-Type-Options: nosniff`.
Delete uploads with their execution and its retention. Reruns and restarts reuse the source execution's files, deduplicated by hash.

### Giving files to agents

Files are opt-in per node, `files: inline | path | none`, defaulting to `none` so existing pipelines are unchanged:
- `inline` appends text files to the input with `--- file: name ---` headers, capped by a character budget. Binary files are listed but not inlined.
- `path` copies the files into a per-run working folder and passes their paths to local CLI runtimes (codex, claude, copilot, cli) and to code nodes through `SIGNALBOX_FILES_DIR`.
- Image and PDF support for API runtimes is a later step, limited to where the provider supports it.

Pipeline nodes can pass files to child executions with `files: inherit`.
Files that match a declared schema (item 7) can be validated on upload.

### UI and API

- List attached files on the execution page with sizes and download links.
- Show which nodes received them.
- Let approvers and manual editors open the files while reviewing.

Acceptance: Uploads respect limits, are stored and served safely, survive reloads, and are removed with their execution. Tests cover validation, name sanitising, the inline budget, path delivery to CLI and code nodes, inheritance by child pipelines, and reuse by restart and rerun. The Docker setup documents that the state volume holds uploads. Demo pipelines use the mock and code runtimes only.


## Suggested delivery order
- Routing and retry foundations, then automated gates.
- Session execution and transcript persistence, then chunks.
- Context assembly, since sessions and ordinary nodes can both benefit from it.
- Parameters and local imports/inheritance, followed by MCP and any provider additions.
- Stage-level recovery and CLI operations, then consider a registry once the schema is stable.
- Structured responses before uploads, so uploaded data files can reuse schema validation.
- Across all phases, add schema and engine tests first, then API/UI support; update the README with the user-facing YAML and behavior. Preserve existing pipeline semantics by making new fields opt-in and providing tests that demonstrate old pipeline files still validate and run.
