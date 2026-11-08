package cli

// Templates written by `signalbox init`.

const configTemplate = `# SignalBox project configuration.
version: 1
project: %s

# Directory containing pipeline YAML files.
pipelines_dir: pipelines

# Directory where execution history is stored.
state_dir: .signalbox

# Maximum number of terminal executions to retain (0 keeps history forever).
execution_retention: 1000

# Maximum number of runtime calls running at once; additional calls queue.
max_concurrent_calls: 4

server:
  host: 127.0.0.1
  port: 8484

# Runtimes map agent "runtime" names to AI backends.
# Types: openai (OpenAI-compatible API), codex, claude, copilot (local agent
# CLIs), cli (any custom command), mock (deterministic echo for demos/tests).
runtimes:
  mock:
    type: mock

  # openai:
  #   type: openai
  #   model: gpt-4o
  #   api_key_env: OPENAI_API_KEY   # key is read from this env var
  #   # base_url: https://api.openai.com/v1   # any OpenAI-compatible endpoint

  # claude:
  #   type: claude                  # runs: claude -p "<prompt>"
  #   # command: claude
  #   # args: ["-p"]
  #   # timeout_seconds: 600

  # codex:
  #   type: codex                   # runs: codex exec "<prompt>"
  #   model: gpt-5.4               # default; pipeline model overrides win
  #   reasoning_effort: high       # model-dependent effort level
  #   speed: standard              # fast requests priority service (extra cost)

  # copilot:
  #   type: copilot                 # runs: copilot -p "<prompt>"

  # custom:
  #   type: cli
  #   command: my-agent
  #   args: ["--quiet"]
  #   prompt_mode: stdin            # "arg" (default) or "stdin"
  #   model_arg: --model
  #   reasoning_effort_arg: --effort # only if the custom CLI supports it
  #   speed_arg: --speed             # receives standard or fast

# Optional polling triggers. A changed, non-empty command output starts the
# named pipeline with that output as input. Watchers run under signalbox serve.
# watchers:
#   - name: issue-feed
#     command: jira
#     args: [issues, list, --project, OPS, --output, json]
#     interval: 5m
#     pipeline: default
`

const defaultPipelineTemplate = `# A demo pipeline showing every SignalBox concept. It uses the mock runtime
# so it works out of the box; point the agents at real runtimes in
# signalbox.yaml when ready.
name: default
description: >-
  Demo flow: a triage agent hands off to an orchestrator that coordinates a
  research stage (two agents in parallel) and a drafting stage, then a human
  approves the draft before the publisher agent finishes.
entry: triage

agents:
  # A standard agent accepts a handoff (when several agents hand off to it,
  # it waits for all of them and runs once) and hands off to one or more
  # agents, or to a single orchestrator.
  - id: triage
    type: standard
    runtime: mock
    description: Classifies the incoming request and frames the task.
    prompt: |
      You are the triage agent. Restate the request as a clear task brief.
    handoff: [coordinator]

  # An orchestrator accepts handoffs from many agents (fan-in) and runs its
  # stages in order. Each stage dispatches to agents in parallel and collects
  # the results of their chains before moving on.
  - id: coordinator
    type: orchestrator
    runtime: mock
    description: Long-running coordinator for research and drafting.
    stages:
      - id: research
        agents: [researcher, analyst]
        prompt: |
          Merge the research notes and analysis into one coherent summary.
      - id: drafting
        agents: [writer]
    handoff: [review]

  - id: researcher
    type: standard
    runtime: mock
    description: Gathers background information.
    prompt: |
      You are the researcher. List the key facts relevant to the brief.
    # No handoff: inside an orchestrator stage this hands back to the
    # orchestrator.

  - id: analyst
    type: standard
    runtime: mock
    description: Analyses risks and constraints.
    prompt: |
      You are the analyst. Identify risks, constraints, and open questions.

  - id: writer
    type: standard
    runtime: mock
    description: Produces the draft.
    prompt: |
      You are the writer. Produce a polished draft from the stage context.

  # An approval node pauses the execution until a human approves or rejects
  # it in the web UI (signalbox serve) or the terminal (signalbox run).
  - id: review
    type: approval
    description: Human review of the draft before publishing.
    handoff: [publisher]

  - id: publisher
    type: standard
    runtime: mock
    description: Final formatting and delivery.
    prompt: |
      You are the publisher. Format the approved draft for delivery.
`

const gitignoreTemplate = `# SignalBox execution state
.signalbox/
`
