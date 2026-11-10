# SignalBox project guide

This folder is a SignalBox project: YAML pipelines of AI agents, code steps,
quality gates and human approvals, run by the `signalbox` executable. Use
this guide to manage the project and to design, write and verify new
pipelines. The [SignalBox README](../README.md) indexes the full reference in
the [help directory](../help/); link to its guides rather than restating them.

## Layout

| Path | Contents |
|------|----------|
| `signalbox.yaml` | Project config: runtimes, MCP servers, registry, uploads, server address. See [Project configuration](../help/configuration.md#project-configuration-signalboxyaml). |
| `pipelines/` | One pipeline per `.yaml` file. Every file here is loaded as a pipeline. |
| `shared/` | Importable templates and agents (`imports:`). Keep shared files out of `pipelines/`. |
| `schemas/` | JSON Schemas (`.json`/`.yaml`) referenced by name without extension. |
| `libs/` | Scripts for `type: code` nodes (`python`, `javascript`, `powershell`). |
| `samples/` | Example inputs and uploads for demos. |
| `registry/` | Local pipeline registry. Published versions are immutable; never edit them. |
| `.signalbox/` | Execution state, database and uploads. Never edit or commit it. |

Available runtimes are those under `runtimes:` in `signalbox.yaml` (currently
`mock` and `codex`). Check it before choosing a runtime; ask before adding one.

## Commands

Run from this folder. Build first from the repository root with
`go build ./cmd/signalbox`, or use `go run ..\cmd\signalbox <command>`.

```powershell
..\signalbox.exe validate                         # config, every pipeline and schema
..\signalbox.exe run <pipeline> --input "..." --param k=v --file samples\x.md [--yes]
..\signalbox.exe serve                            # web UI on the configured port
.\rebuild.ps1                                     # rebuild and restart the server
```

`--yes` auto-approves approval gates. Execution management (`list`, `show`,
`approve`, `reject`, `retry`, `restart`, `rerun`, `cancel`) and `registry`
commands talk to a running `serve`; see [Command line](../help/command-line.md#command-line)
and [Pipeline registry](../help/registry.md#pipeline-registry).

Always run `validate` after changing anything in `pipelines/`, `shared/`,
`schemas/` or `signalbox.yaml`, and fix every error before finishing.

## Creating a pipeline

1. **Clarify.** Before writing YAML, establish: the input (text, JSON, uploaded
   files), the deliverable, who must approve it, which runtimes are allowed,
   whether variants need `params`, and the acceptable cost in LLM calls. Ask
   when these are unclear.
2. **Design the graph.** Sketch nodes and handoffs. Prefer a deterministic
   `code` step for anything computable (parsing, statistics, validation), and
   reserve LLM calls for judgement. Count the LLM calls of a successful pass
   and state it.
3. **Pick node types** (see [Agent types](../help/pipelines.md#agent-types)):

   | Need | Use |
   |------|-----|
   | One LLM step | `standard` |
   | Independent specialists in parallel, then collect | `orchestrator` with `stages` |
   | Deterministic transform or check | `code` |
   | Schema-checked JSON with routing on fields | `output.schema` plus `condition: output.x == ...` |
   | Automated quality check with bounded rework | `llm_gate` with `reject-to` and `max_reworks` |
   | Agents debating to agreement | `session` |
   | Human sign-off or edit | `approval` / `manual_edit` |
   | Reuse a whole pipeline or a section of one | `pipeline` |

4. **Write the YAML** in `pipelines/<name>.yaml`, following the conventions
   below. Add schemas to `schemas/` and scripts to `libs/`.
5. **Verify.** Run `validate`. Test scripts directly with sample stdin (and
   `SIGNALBOX_FILES_DIR` for file-reading scripts), including their error
   paths. For a dry run without LLM cost, temporarily point the shared
   template's `runtime` at `mock` (it returns schema-conforming samples), run
   with `--yes`, then restore it. For changes to engine behaviour, add a Go
   test in `../internal/engine/` modelled on `qa_pipeline_test.go` or
   `manuscript_pipeline_test.go`.
6. **Document.** Add a header comment to the pipeline (purpose and a sample
   `signalbox run` command) and a demo bullet in the relevant README section.

## Pipeline conventions

- Start with `name` (lowercase letters, digits, `-`, `_`; required for
  registry publishing), `description`, then `files`, `params`, `entry`,
  `templates`, `agents`.
- Put the shared `runtime` and `retry` (`{count: 2, backoff: 2s}`) in a
  template and `extends` it, so one edit retargets the pipeline. Session
  participants cannot set `retry`; use a separate template without it and set
  `retry` on the session.
- Write prompts as a role, the task, explicit criteria, the output format and
  how to handle rejection notes. Tell agents to cite evidence, never invent
  facts, and list uncertainties instead of guessing.
- Give every `llm_gate` and approval a concrete checklist or `description`.
  Feed gates the original requirements with `additional-input` so they judge
  against the source, not a summary.
- Use `format: true` on nodes whose Markdown a person reads.
- Name IDs in `snake_case` after their role (`structure_editor`, `letter_check`).
- Exemplars: [manuscript-review.yaml](pipelines/manuscript-review.yaml)
  (uploads, params, code profile, orchestrator, schema, gate, approval) and
  [QA.yaml](pipelines/QA.yaml) (templates, per-planner gates, session, revision
  loop).

## Gotchas

- **Handoffs:** a handoff to an orchestrator must be its only target. An
  orchestrator without a `prompt` concatenates stage outputs. Downstream
  nodes that need earlier material (for example a profile) should use
  `additional-input`.
- **Rework:** `reject-to` reruns the target with its saved input plus the
  rejection notes. Add `context: {strategy: own, window: 1}` so it also sees
  its latest draft. Targets must be in the same stage/scope and lead back to
  the gate. `max_reworks` is 1–10 when rework is configured.
- **Gates:** an `llm_gate` must return `decision` (`approve`/`reject`) and
  `feedback`. A custom gate schema must keep both.
- **Schemas:** local `$ref` only. `on_invalid: repair` (agents) re-asks with
  the validation errors; code steps default to `fail`.
- **Params:** `${{ params.x }}` works in prompts, descriptions, `model`,
  handoff `input` templates, stage prompts and chunk titles, but not in IDs,
  runtimes or routes. A param is `required` or has a `default`, never both.
  Values are stored in plain text, so never pass secrets as params.
- **Files:** nodes see uploads only when they set `files: inline` (text,
  within `files_budget`, any runtime) or `files: path` (by path, CLI runtimes
  only, not `openai`). Binary files such as `.docx` are never inlined; have
  the agent or script extract the text. PDF is not in the default extensions.
  Child pipelines need `files: inherit`.
- **Sessions:** participants are plain `standard` agents in exactly one
  session, with no `handoff`, `reject-to`, `additional-input` or `retry`.
  Always route `on_no_consensus` (or `always`), or the run is rejected when
  rounds run out.
- **Templates:** maps deep-merge; scalars, lists and prompts replace.
  Overriding inherited `context` with `strategy: none` also needs `window: 0`.
- **Code steps:** `code-source` and the working directory are this folder. The
  input arrives on stdin and the output goes to stdout. With `codes`, all of
  `success`, `fail` and `retry` are required. Without them, a nonzero exit
  waits for a manual retry. Scripts run unsandboxed as the server user.
- **Long prompts:** above about 24,000 characters only `codex` and `claude`
  switch to stdin; other runtimes fail. Keep judge inputs lean.
- **Watchers** cannot start pipelines with required files or required params
  without defaults.

## Managing the project

- Never put secrets in YAML. Use `api_key_env` for runtimes and `env_from`
  for MCP servers. MCP tools must be listed explicitly; wildcards are invalid.
- Change `signalbox.yaml` only when asked. Restart `serve` afterwards (or run
  `rebuild.ps1`).
- Before deleting or renaming a pipeline, search for `pipeline:` nodes,
  `imports:` and watchers that reference it.
- Publishing to the registry bundles imports, scripts and schemas but not
  chained pipelines; publish those too. Bump the `version:` rather than
  editing a published one.
