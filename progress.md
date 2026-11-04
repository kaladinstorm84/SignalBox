# SignalBox progress

Tracking file for changes between chat sessions.

## 2026-11-04 — Initial build (v0.1)

Greenfield implementation of SignalBox in Go (module `github.com/gitea/signalbox`,
Go 1.25, single dependency: `gopkg.in/yaml.v3`).

### Done

- **Pipeline schema & validation** (`internal/pipeline`)
  - Node types: `standard`, `orchestrator` (with ordered `stages`), `approval`.
  - Validation: unknown handoff targets, self-handoff, duplicate ids, cycles
    (over handoff + stage edges), unreachable nodes, orchestrator-must-be-sole
    handoff target, stages may not reach another orchestrator, orchestrators
    need stages, standard agents need runtimes.
- **Project config** (`internal/config`) — `signalbox.yaml` with runtimes map,
  pipelines dir, state dir, server host/port; defaults applied on load.
- **Runtime adapters** (`internal/runtime`)
  - `openai`: OpenAI-compatible chat completions (configurable `base_url`,
    model, key env var).
  - `codex` / `claude` / `copilot`: presets over a generic `cli` adapter
    (command, args, prompt via arg or stdin, timeout).
  - `mock`: deterministic echo used by the scaffolded demo pipeline and tests.
- **Execution engine** (`internal/engine`)
  - Parallel fan-out handoffs; chains of agents and approvals.
  - Orchestrators: singleton per execution, fan-in join (waits for all inbound
    handoffs), sequential stages with parallel dispatch and chain collection,
    optional per-stage and final synthesis prompts via the orchestrator runtime.
  - Approval gates block on human decision; rejection stops the execution.
  - Cancel support; failures cancel in-flight work.
  - Persistence: one JSON file per execution under `.signalbox/executions/`
    (atomic writes); unfinished runs marked `interrupted` on reload.
- **Web server & UI** (`internal/server`) — JSON API + embedded single-page UI
  (executions list w/ live polling, execution detail w/ per-run timeline,
  orchestrator stage breakdown, approve/reject with comment, new-run form,
  pipelines view).
- **CLI** (`internal/cli`, `cmd/signalbox`) — `init` (scaffolds signalbox.yaml,
  pipelines/default.yaml, .gitignore), `validate`, `serve [--addr]`,
  `run <pipeline> [--input] [--yes]` with terminal approvals, `version`.
- **Tests** — 15 tests across pipeline validation and engine behaviour
  (linear chain, fan-out, approval approve/reject, orchestrator fan-in +
  stage ordering + synthesis, failure propagation, cancel, store round-trip).
  All passing with `go test ./...`. (`-race` unsupported on this Pi's kernel
  VMA config.)
- **Smoke tested** — `init` → `validate` → `run` (terminal approval) →
  `serve` (UI 200, start execution via API, approve via API, completed).

### Backup

- Pushed to http://192.168.68.56:3000/gitea/SignalBox.git (`main`); machine
  stores credentials via git credential store for future pushes.

### Fixes during build

- `signalbox run <pipeline> --input x`: Go's `flag` stops at the first
  positional arg, so flags after the pipeline name were ignored; now re-parses
  the remainder. Added `--yes` auto-approve for non-interactive runs.

### Not done / ideas for later

- Resuming in-flight executions across server restarts (currently marked
  `interrupted`).
- Streaming/SSE updates instead of 2s polling.
- Per-approval assigned approvers / auth on the web UI.
- Editable payloads at approval gates.
- `go vet` clean; no linter beyond vet configured.
