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
| `standard` | Accepts a handoff (or several ? see joins below), runs its prompt against a runtime, then hands off to one or more agents — or to a single orchestrator. |
| `orchestrator` | A long-running coordinator. It accepts handoffs from **many** agents (fan-in: it waits until every inbound handoff has arrived), then runs its **stages** in order. Each stage dispatches to agents in parallel and collects the terminal output of every chain it started before moving on. A stage (or the orchestrator itself) can define a `prompt` to synthesise the collected results using the orchestrator's runtime. |
| `approval` | A human gate. Approval passes the payload through unchanged. Rejection restarts work after the last upstream approved gate in the same stage/scope, with reviewer notes appended to its input. |
| `code` | Executes a local script with its input on stdin, then hands stdout to downstream nodes. No AI runtime is required. |

### Handoffs

- A node's `handoff` list names the nodes that receive its output. Multiple
  targets run as parallel branches.
- A handoff to an orchestrator must be the *sole* target.
- An agent with no `handoff` terminates its chain. Inside an orchestrator
  stage, a terminating chain hands its output back to the orchestrator;
  outside one, the output becomes a **result** of the execution.
- Chains of agents and approvals can be nested inside orchestrator stages.
- **Joins:** when several agents hand off to the same standard or approval
  agent, it waits for all of them and runs once on the combined input (each
  part headed `--- handoff from <agent> ---`). Joins are counted per scope:
  inside a stage, only handoffs from that stage's chains count. For example,
  two planners in a stage can both hand off to a judge, which then hands back
  to the orchestrator.
- **Additional input:** an agent can name other nodes whose completed output
  it also needs. The engine waits for those outputs and appends each under an
  `--- additional input from <agent> ---` heading, alongside normal handoffs.
  For example, a judge can receive planner handoffs plus the original
  requirements:

  ```yaml
  - id: judge
    runtime: codex
    additional-input: [requirements_loader]
  ```
- **Formatted output:** set `format: true` on an agent to render its Markdown
  output in the execution detail view. The raw Markdown remains available in a
  collapsible section; raw HTML in Markdown is not rendered.

### Code Steps

```yaml
- id: Verify
  type: code
  language: python
  code-source: libs/verify.py
  timeout_seconds: 30
  codes:
    success: 0
    fail: 1
    retry: 2
  handoff: [security_judge]
```

Connect an upstream node with `handoff: [Verify]`, or list the code node in an
orchestrator stage's `agents`. `code-source` is relative to the project directory
containing `signalbox.yaml`; scripts also run with that working directory.
Supported languages are `python`, `javascript` (Node.js), and `powershell`;
the corresponding `python`, `node`, or `powershell` command must be on `PATH`.
Scripts run locally with the server user's permissions, not in a sandbox.

The full assembled handoff input is sent as UTF-8 text on **stdin**, including
any `additional-input` sections. Write the result to **stdout** and diagnostics
to **stderr**. With `codes`, the mapped `success` hands stdout downstream and
`fail` immediately fails the execution. The mapped `retry` acts like rejection:
it restarts work after the last upstream approved gate in the same stage/scope,
or retries the immediate upstream agents if no earlier approval exists. Both
stdout and stderr become rejection feedback appended to the rework input.
The rejected code run remains in the timeline and does not hand off a result.
Without upstream work to retry, rejection ends the execution.

All three mappings are required when `codes` is present and must have distinct,
non-negative values. Without `codes`, nonzero exits retain the manual-retry
behavior. Unmapped exits and process errors (including timeouts) also pause
for manual retry. The script is reloaded on each call. Code steps share the
runtime concurrency cap and cancellation handling; the default timeout is
600 seconds.

For structured input, an upstream node can return JSON and the script can parse
it directly (without additional labeled handoff sections):

```python
import json
import sys

data = json.load(sys.stdin)
if not data.get("tests"):
    print("Validation failed: no tests supplied", file=sys.stderr)
    sys.exit(1)
print(json.dumps({"validated": True, "tests": data["tests"]}))
```

The QA example's `libs/verify.py` currently checks for non-empty planner output
and passes it through; replace its body with your validation rules.

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

By default the prompt is passed as the final argument. Windows limits a
command line to 32,767 characters, so a prompt longer than about 24,000
characters (e.g. a judge receiving every planner's output) is piped through
stdin instead for `codex` (`codex exec -`) and `claude` (`claude -p`). Other
runtimes fail with a clear error in that case; set `prompt_mode: stdin` if the
command can read its prompt from stdin.

## Getting started

```bash
go install github.com/kaladinstorm84/SignalBox/cmd/signalbox@latest   # or: go build ./cmd/signalbox

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
  into it. Opening New run (or clicking it again) reloads pipeline YAML from
  disk, so added and edited pipelines are available without restarting the server.
- **Pipelines** — the loaded definitions: agents, types, runtimes, handoffs,
  and stages.
- **Settings** — edit the project configuration as YAML. Saving validates the
  configuration, runtime definitions, and pipeline references while preserving
  comments and whitespace. Reload reads the current file from disk; a stale
  save is rejected rather than overwriting external edits. Restart the server
  after saving to apply runtime, concurrency, watcher, and server settings.
- Both pages draw each pipeline as a track diagram: agents that run one after
  another sit on a single line, while agents that run in parallel (orchestrator
  stages and handoffs to several agents) split onto parallel tracks that rejoin
  where their outputs are collected, including at an agent that joins several
  branches.
- Approval gates surface directly in the execution view with
  approve/reject buttons and an optional comment.
  Rejection preserves the rejected review in the timeline and sends work back
  after the last upstream approved gate in the same stage/scope. If there is
  no earlier approved gate, the immediate upstream agents are retried. Notes
  are appended under `--- rejection notes from <gate> ---`. If there is no
  upstream work to retry (for example, an approval is the entry), rejection
  still ends the execution.
- When an agent call fails, that run pauses as `waiting_retry` with a
  **Retry** button; parallel branches keep running, but nothing downstream of
  the failure proceeds. Retry re-invokes the agent with the same input; use
  **Cancel** to give up. `signalbox run` prompts `retry? [y/N]` instead (with
  `--yes`, failures are not retried and the execution fails).
- **Rerun** starts a new execution with the same pipeline and input, using the
  pipeline definition currently on disk. **Delete** removes a finished
  execution and its history file; cancel a running execution first.

Execution history is persisted as JSON under `.signalbox/executions/`, so it
survives restarts (in-flight runs are marked `interrupted`).
Interrupted agent cards offer **Retry from here**, which starts a new execution
at that call with its saved input. Completed earlier outputs remain available
to `additional-input` dependencies; completed branches are not rerun. **Rerun**
continues to start the whole pipeline from its original input.

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
max_concurrent_calls: 4
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

`max_concurrent_calls` limits simultaneous runtime invocations across all
executions, including orchestrator synthesis. Calls above the limit wait for a
slot; the default is `4` and the value must be positive.

### Watchers

Watchers are project-level pollers started by `signalbox serve`. Each runs a
command directly (without a shell) at the configured interval. When its
trimmed stdout is non-empty and differs from the previous successful poll,
SignalBox starts the named pipeline and passes stdout as the pipeline input.
Unchanged output is suppressed; the last output hash is stored under
`.signalbox/watchers/` so restarting the server does not replay it. A failed
command is logged and retried on the next interval. The default command timeout
is two minutes.

```yaml
watchers:
  - name: jira-queue
    command: jira
    args: [issues, list, --jql, "project = OPS AND status = 'To Do'", --output, json]
    interval: 5m
    timeout_seconds: 30
    pipeline: default
```

The command must be available on `PATH` (or use an absolute path). Keep its
output stable between polls unless a new pipeline run is intended.

## HTTP API

| Method & path | Purpose |
|---------------|---------|
| `GET /api/pipelines` | Loaded pipeline definitions |
| `GET /api/settings` | Project configuration YAML and file version |
| `PUT /api/settings` | Validate and save project YAML: `{"content": "...", "version": "..."}` |
| `GET /api/executions` | Execution summaries (newest first) |
| `POST /api/executions` | Start a run: `{"pipeline": "default", "input": "..."}` |
| `GET /api/executions/{id}` | Full execution detail |
| `POST /api/executions/{id}/approve` | Resolve a gate: `{"run_id": "...", "approved": true, "approved_by": "...", "comment": "..."}` |
| `POST /api/executions/{id}/retry` | Retry a failed agent call awaiting retry: `{"run_id": "..."}` |
| `POST /api/executions/{id}/restart` | Start a new execution from an interrupted standard-agent call: `{"run_id": "..."}` |
| `POST /api/executions/{id}/cancel` | Cancel a running execution |
| `POST /api/executions/{id}/rerun` | Start a new execution with the same pipeline and input; returns `{"id": "..."}` |
| `DELETE /api/executions/{id}` | Delete a finished execution (409 while it is still running) |

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
