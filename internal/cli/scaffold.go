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

const defaultPipelineTemplate = `# A demo using mock AI agents, a Python plugin and a knowledge base in sequence.
# Requires Python 3 available as python on PATH; no AI credentials are needed.
# Configure real runtimes in signalbox.yaml when ready.
name: default
description: >-
  Demo flow: triage passes the brief to research, analysis, then drafting in
  sequential orchestrator stages. A Python plugin counts the completed draft
  before a human approves it and the publisher agent finishes.
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
  # stages in order. Stages run sequentially; agents within each stage run in
  # parallel and their chains finish before the next stage begins.
  - id: coordinator
    type: orchestrator
    runtime: mock
    description: Runs research, analysis and drafting in dependency order.
    stages:
      - id: research
        agents: [researcher]
        prompt: |
          Summarize the research findings for the analyst. Preserve relevant
          facts and clearly identify anything that still needs investigation.
      - id: analysis
        agents: [analyst]
        prompt: |
          Analyze the research findings above. Summarize the evidence, risks
          and open questions for the writer.
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
      You are the analyst. Analyze the research findings in your input.
      Identify evidence, risks, constraints, and open questions.

  - id: writer
    type: standard
    runtime: mock
    knowledge_bases: [project-handbook]
    handoff: [word_counter]
    description: Produces the draft using the project's writing policy.
    prompt: |
      You are the writer. Produce a polished draft from the stage context.
      Follow the writing policy in the attached project handbook.

  # Counts the writer's output and passes the draft along with the count.
  - id: word_counter
    type: plugin:word-count
    description: Counts the completed draft before human review.

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

const wordCountPluginTemplate = `description: Count words in the handoff text.
language: python
entry: main.py
timeout_seconds: 30
`

const wordCountScriptTemplate = `import sys

text = sys.stdin.read()
sys.stdout.write(f"Draft word count: {len(text.split())}\n\n--- Draft ---\n{text}")
`

const projectHandbookTemplate = `# Writing policy

- Keep drafts concise and relevant to the original request.
- Distinguish facts from assumptions and flag missing information.
- Use Markdown headings and short bullet lists when helpful.
`

const gitignoreTemplate = `# SignalBox execution state
.signalbox/
`

const agentsTemplate = `# SignalBox Project Guide

This directory is a SignalBox project. Pipelines are YAML files that define
agents, code steps, script plugins, quality gates and human approvals.
Knowledge bases provide reusable reference documents for AI agents.

## Project layout

- signalbox.yaml configures runtimes, paths and the server.
- pipelines/ contains executable pipeline definitions.
- shared/ contains reusable pipeline and agent templates.
- schemas/ contains JSON Schemas used by pipelines.
- libs/ contains scripts used by code steps.
- plugins/<name>/ contains a plugin.yaml descriptor, entry script and helpers.
- knowledge-bases/<name>/ contains the text documents in a knowledge base.
- .signalbox/ stores local execution state; do not edit or commit it.

The starter default pipeline passes work through sequential research,
analysis and drafting stages, then uses the word-count plugin on the draft.
The project-handbook knowledge base is attached to the writer.
Install Python 3 as python on PATH before running it; its AI agents use
mock, so no AI credentials are needed.

## Working in this project

- Check the existing config and pipeline conventions before making changes.
- Keep changes within the requested project files; do not add runtimes or
  change project configuration unless asked.
- Never put API keys, passwords or other secrets in project files. Use
  environment variables for credentials.
- Prefer deterministic code steps for parsing, calculations and validation.
  Prompts should specify the task, evidence requirements and expected output.
- Run signalbox validate after changing signalbox.yaml, pipelines/, shared/,
  schemas/, plugins/ or knowledge-bases/, and address all reported errors.
- Test code-step and plugin scripts with representative input, including error
  cases. Validation does not execute scripts or read attached knowledge text;
  also run a small pipeline that uses the changed plugin or knowledge base.

## Pipeline conventions

- Keep each pipeline in its own .yaml file under pipelines/.
- Use descriptive agent IDs and prompts that state the role, task and output
  format. Ask agents to cite evidence and identify uncertainty rather than
  inventing facts.
- Give approval and quality-gate nodes concrete review criteria.
- Reuse shared templates where appropriate, and keep reusable files outside
  pipelines/ so they are not loaded as standalone pipelines.
- Include a sample command when documenting a pipeline:
  signalbox run <pipeline> --input "..."

## Creating script plugins

Use a plugin for reusable deterministic code; use a code step in libs/ for
pipeline-specific scripts. Create plugins/<name>/ with a lowercase name using
letters, digits, _ or -, starting with a letter or digit (up to 64 characters).
Avoid Windows device names such as con, nul and com1. The folder supplies the
plugin name; do not add a name field to the descriptor.

Define the execution contract in plugin.yaml:

~~~yaml
# plugins/word-count/plugin.yaml
` + wordCountPluginTemplate + `~~~

Supported languages are python, javascript and powershell. Install the matching
interpreter (python, node or powershell) and dependencies separately; SignalBox
does not install them. The entry must be a regular file inside the plugin
folder. The timeout defaults to 600 seconds. Optional codes must define all
three distinct, non-negative exit codes:

    codes: {success: 0, fail: 1, retry: 2}

Read handoff input from stdin, write the result to stdout, and send diagnostics
to stderr. Keep stdout suitable for downstream agents or an output schema.

~~~python
# plugins/word-count/main.py
` + wordCountScriptTemplate + `~~~

Scripts run from the project root, not the plugin folder. Resolve helper files
relative to the entry script (__file__ in Python, __dirname or import.meta.url
in JavaScript, $PSScriptRoot in PowerShell). Keep generated output, caches,
virtual environments and secrets outside the plugin folder.

Reference the plugin as a node type:

~~~yaml
# pipelines/count-words.yaml
name: count-words
entry: count
agents:
  - id: count
    type: plugin:word-count
~~~

The descriptor owns language, entry, timeout and exit mappings. Do not repeat
code fields, prompts, runtime/model settings or stages on the node. Pass
invocation-specific data through the handoff; there are no plugin parameters.
Handoffs, fixed retries, additional-input, files: path and output schemas work
as for code steps. Scripts execute with the user's permissions, not in a
sandbox; review code before running it.

Run signalbox validate, then:
signalbox run count-words --input "Hello reusable plugins"
The result begins with "Draft word count: 3" and includes the original text;
also check empty input and error paths.

## Creating knowledge bases

Create knowledge-bases/<name>/ and put UTF-8 text documents directly inside
it; no manifest or config entry is needed. Prefer the same lowercase,
registry-safe naming rules as plugins. Alternatively, create a base and edit
or upload documents on the web UI's Knowledge bases page.

~~~markdown
# knowledge-bases/project-handbook/policy.md
` + projectHandbookTemplate + `~~~

Supported document extensions are .txt, .md, .csv, .tsv, .json, .yaml, .yml,
.xml and .log. Use regular files, not symlinks or nested document folders.
Keep each document within 1 MiB for editor saves, uploads and registry
publishing. Do not store credentials or unrelated private data.

Attach bases by folder name with knowledge_bases:

~~~yaml
# pipelines/handbook-answer.yaml
name: handbook-answer
entry: assistant
agents:
  - id: assistant
    runtime: mock
    knowledge_bases: [project-handbook]
    prompt: Answer using the references and cite their document names.
~~~

Use knowledge_bases on standard agents, llm_gate nodes, orchestrators or
sessions; session participants can also select their own bases. Orchestrators
use the references for synthesis calls. Code and plugin nodes do not use them.

SignalBox appends the complete attached text on every runtime call; this is
not semantic search or retrieval. Keep references focused: the combined
knowledge, including document headers, must fit within 5 MiB per invocation
and the chosen model's context budget. Documents are reread when agents run,
so edits apply to subsequent calls.

Run signalbox validate, then:
signalbox run handbook-answer --input "What writing policy should I follow?"
The scaffold's mock runtime echoes the input and references without an AI
call. Check that policy.md and its text appear; use an already configured
real runtime when an actual answer is needed.

## Sharing plugins and knowledge bases

Publish only when asked, using the configured registry or --registry PATH:

~~~text
signalbox registry publish-plugin word-count --version 1.0.0
signalbox registry publish-knowledge project-handbook --version 1.0.0
signalbox registry show <name>@1.0.0
signalbox registry install <name>@1.0.0
~~~

Pipeline, plugin and knowledge packages share one name namespace. Published
versions are immutable. Publishing a pipeline also bundles its referenced
plugin folders and knowledge documents. Every regular file in a plugin folder
is packaged, so remove private files, caches and generated output first.
Inspect packages before installing or executing them; hashes establish
integrity, not trust. Registry operations do not execute plugin code.
`
