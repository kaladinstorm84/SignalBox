# SignalBox

SignalBox is a YAML based AI agent workflow system with human approval stages.
You describe a pipeline of agents in YAML, point each agent at a runtime
(Codex, Claude, Copilot, or any OpenAI-compatible API), and SignalBox executes
the chain — pausing at approval gates until a human signs off in the web UI or
the terminal.

Built in Go; ships as a single static binary with the web UI embedded.

## Concepts

### Agent types

| Type | Behaviour |
|------|-----------|
| `standard` | Accepts a handoff from a single agent/orchestrator, runs its prompt against a runtime, then hands off to one or more agents — or to a single orchestrator. |
| `orchestrator` | A long-running coordinator. It accepts handoffs from **many** agents (fan-in: it waits until every inbound handoff has arrived), then runs its **stages** in order. Each stage dispatches to agents in parallel and collects the terminal output of every chain it started before moving on. A stage (or the orchestrator itself) can define a `prompt` to synthesise the collected results using the orchestrator's runtime. |
| `approval` | A human gate. The execution pauses until someone approves or rejects it. Approval passes the payload through unchanged; rejection stops the execution. |

### Handoffs

- A node's `handoff` list names the nodes that receive its output. Multiple
  targets run as parallel branches.
- A handoff to an orchestrator must be the *sole* target.
- An agent with no `handoff` terminates its chain. Inside an orchestrator
  stage, a terminating chain hands its output back to the orchestrator;
  outside one, the output becomes a **result** of the execution.
- Chains of agents and approvals can be nested inside orchestrator stages.

### Runtimes

Configured in `signalbox.yaml` and referenced by name from agents:

| Type | Backing |
|------|---------|
| `openai` | Any OpenAI-compatible chat completions API (`base_url` configurable, key read from an env var). |
| `codex` | `codex exec "<prompt>"` (local CLI). |
| `claude` | `claude -p "<prompt>"` (local CLI). |
| `copilot` | `copilot -p "<prompt>"` (local CLI). |
| `cli` | Any custom command; prompt via final argument or stdin (`prompt_mode`). |
| `mock` | Deterministic echo for demos and tests. |

The CLI presets just set a default command/args — everything (command, args,
prompt delivery, timeout) is overridable per runtime.

## Getting started

```bash
go install github.com/gitea/signalbox/cmd/signalbox@latest   # or: go build ./cmd/signalbox

signalbox init my-project     # creates signalbox.yaml + pipelines/default.yaml
cd my-project
signalbox validate            # check config and all pipelines
signalbox serve               # web UI on http://127.0.0.1:8484
signalbox run default --input "Hello SignalBox"   # terminal run
```

`signalbox run` prompts for approvals on the terminal; pass `--yes` to
auto-approve every gate (useful in scripts/CI).

## The web UI (`signalbox serve`)

- **Executions** — live list of every run with status and pending-approval
  badges; click through to a per-agent timeline showing inputs, outputs,
  orchestrator stages, and errors.
- **New run** — pick a pipeline, preview its agent flow, and send an input
  into it.
- **Pipelines** — the loaded definitions: agents, types, runtimes, handoffs,
  and stages.
- Approval gates surface directly in the execution view with
  approve/reject buttons and an optional comment.

Execution history is persisted as JSON under `.signalbox/executions/`, so it
survives restarts (in-flight runs are marked `interrupted`).

## Pipeline YAML

```yaml
name: default
description: Demo flow.
entry: triage

agents:
  - id: triage
    type: standard
    runtime: mock
    prompt: |
      You are the triage agent. Restate the request as a clear task brief.
    handoff: [coordinator]

  - id: coordinator
    type: orchestrator
    runtime: mock
    stages:
      - id: research
        agents: [researcher, analyst]   # run in parallel, results collected
        prompt: Merge the research notes and analysis into one summary.
      - id: drafting
        agents: [writer]
    handoff: [review]

  - id: researcher
    runtime: mock
    prompt: You are the researcher. List the key facts.

  - id: analyst
    runtime: mock
    prompt: You are the analyst. Identify risks and open questions.

  - id: writer
    runtime: mock
    prompt: You are the writer. Produce a polished draft.

  - id: review
    type: approval
    description: Human review of the draft before publishing.
    handoff: [publisher]

  - id: publisher
    runtime: mock
    prompt: You are the publisher. Format the approved draft for delivery.
```

Validation catches unknown handoff targets, cycles, unreachable agents,
missing runtimes, orchestrators without stages, orchestrator handoffs mixed
with other targets, and stage chains that leak into another orchestrator.

## Project configuration (`signalbox.yaml`)

```yaml
version: 1
project: my-project
pipelines_dir: pipelines
state_dir: .signalbox
server:
  host: 127.0.0.1
  port: 8484
runtimes:
  mock:
    type: mock
  openai:
    type: openai
    model: gpt-4o
    api_key_env: OPENAI_API_KEY
  claude:
    type: claude          # claude -p "<prompt>"
  codex:
    type: codex           # codex exec "<prompt>"
  copilot:
    type: copilot         # copilot -p "<prompt>"
```

## HTTP API

| Method & path | Purpose |
|---------------|---------|
| `GET /api/pipelines` | Loaded pipeline definitions |
| `GET /api/executions` | Execution summaries (newest first) |
| `POST /api/executions` | Start a run: `{"pipeline": "default", "input": "..."}` |
| `GET /api/executions/{id}` | Full execution detail |
| `POST /api/executions/{id}/approve` | Resolve a gate: `{"run_id": "...", "approved": true, "approved_by": "...", "comment": "..."}` |
| `POST /api/executions/{id}/cancel` | Cancel a running execution |

## Development

```bash
go build ./...
go test ./...
```

Layout:

- `cmd/signalbox` — CLI entry point
- `internal/cli` — commands (`init`, `validate`, `serve`, `run`) and scaffolding templates
- `internal/config` — `signalbox.yaml` loading
- `internal/pipeline` — pipeline YAML schema and validation
- `internal/engine` — execution engine (handoffs, orchestrator stages, approvals) and JSON persistence
- `internal/runtime` — runtime adapters (OpenAI API, agent CLIs, mock)
- `internal/server` — HTTP API and embedded web UI
