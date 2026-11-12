# Runtimes, models and MCP access

[Back to SignalBox](../README.md)

### Model overrides

Set AI call defaults per named runtime in `signalbox.yaml`:

```yaml
runtimes:
  codex:
    type: codex
    model: gpt-5.4
    reasoning_effort: high
    speed: standard
  claude:
    type: claude
    model: opus
    reasoning_effort: high
    speed: standard
  openai:
    type: openai
    model: gpt-5.4
    reasoning_effort: high
    speed: fast
    api_key_env: OPENAI_API_KEY
```

`model` is the default for all calls through that runtime, including session
participants, orchestrator synthesis, and structured-output repair calls.
An agent's grouped `runtime.model`, `runtime.effort`, and `runtime.speed` take
precedence over the selected runtime's defaults. If neither sets an option, the
provider/CLI default is preserved. Restart the server after changing runtime settings.

Choose the same options for the initial call in pipeline YAML:

```yaml
agents:
  - id: researcher
    runtime:
      provider: codex
      model: gpt-5.4
      effort: high
      speed: standard
    prompt: Research the supplied task.
```

These per-node settings apply to standard agents, LLM gates and orchestrator
synthesis calls. Session participants set their own initial options. They may
use pipeline parameters, and are validated against the selected runtime.
See [fallback retries](execution.md#runtime-retries) to change these settings
automatically after failures.

`runtime.provider` selects an entry in `signalbox.yaml`'s `runtimes` map, such as
`codex`, `codex-fast`, or `company-openai`; it does not create a provider.
The project-level runtime definitions above still use `type` and
`reasoning_effort`. The grouped pipeline syntax uses the shorter `effort`.

The original `runtime: codex` shorthand and top-level `model` remain supported;
top-level `reasoning_effort` and `speed` are also accepted with scalar runtimes.
Do not mix grouped runtime settings with top-level model/effort/speed settings:
put all call settings inside the `runtime` mapping. Unknown grouped keys and
invalid or unsupported settings fail validation.

### Streaming and timeouts

OpenAI-compatible endpoints using SSE and CLI processes that emit stdout
incrementally stream visible generated output to execution details. In
orchestrator runs, that text is saved as a log under its stage; otherwise it is
saved on the agent run. A CLI that buffers stdout until completion, or another
runtime without streaming support, records its output when the call returns.
These logs are response text, not private model reasoning.

Codex calls use `codex exec --json` when streaming, so session activity, tool
start/completion statuses and agent messages appear during the call. Only the
final agent message is handed downstream; JSON events and activity markers are
not part of the article or structured response. Reasoning events are omitted.
Codex emits messages at event boundaries, not necessarily token by token.
Small updates are saved at least once per second even if no new output arrives;
the execution page displays them on its existing two-second refresh.

Each runtime's `timeout_seconds` in `signalbox.yaml` sets its default timeout
(600 seconds when omitted). An AI node can override that value in pipeline
YAML; the override applies to each runtime call and retry for that node:

```yaml
- id: researcher
  runtime: codex
  timeout_seconds: 900
  prompt: Research the supplied task.
```

An omitted or zero node timeout uses the selected runtime's configured
timeout. This bounds the complete call, including time spent waiting for
streamed output.

`speed` accepts `standard` or `fast`. Fast requests provider priority service,
**not** less reasoning or a cheaper model; it may incur additional charges and
requires provider/model/account support. Codex receives `service_tier`, Claude
receives a per-call `fastMode` setting, and OpenAI receives `service_tier:
priority` (`default` for standard). Provider rejection or CLI failure is surfaced
as a runtime error. Claude may apply its own fast-mode eligibility/cooldown rules.

`reasoning_effort` is independent of speed. Supported values:

| Runtime | Reasoning effort | Speed |
|---------|------------------|-------|
| `codex` | `none`, `minimal`, `low`, `medium`, `high`, `xhigh`, `max`, `ultra` | `standard`, `fast` |
| `claude` | `low`, `medium`, `high`, `xhigh`, `max`, `ultracode` | `standard`, `fast` |
| `openai` | `none`, `minimal`, `low`, `medium`, `high`, `xhigh` | `standard`, `fast` |
| `copilot` | Not supported by this preset | Not supported by this preset |
| `cli` | Requires `reasoning_effort_arg` | Requires `speed_arg` |

Actual effort levels depend on the selected model and installed CLI version.
Unsupported configuration fails validation rather than being silently ignored.
The Copilot preset supports `model`; it does not assume undocumented effort or
priority flags. For a custom CLI that supports those options, configure explicit
flags:

```yaml
runtimes:
  custom:
    type: cli
    command: my-agent
    model: my-model
    model_arg: --model
    reasoning_effort: high
    reasoning_effort_arg: --effort
    speed: fast
    speed_arg: --speed
```

Each configured flag receives its value as a separate process argument.

`standard`, `llm_gate`, and `orchestrator` agents can set `runtime.model` (it may use
a param). The `codex`, `claude`, and `copilot` runtimes pass it as
`--model`, `openai` uses it instead of the runtime's `model`, and `mock`
ignores it. A `cli` runtime needs `model_arg` (for example `model_arg:
--model`) or validation fails. Session participants use their own settings.
The execution's **Runtime calls** section shows the effective provider, model,
effort, speed and errors for each call and retry. The HTTP API preserves its
flat `runtime`/`model` fields, with `reasoning_effort` and `speed` alongside them.

### MCP servers

Agents on `codex`, `claude`, and `copilot` runtimes can be given Model Context
Protocol servers. This is opt-in at two levels. The project registers servers
and the tools they may expose:

```yaml
# signalbox.yaml
mcp:
  enabled: true
  servers:
    github:
      command: github-mcp-server
      args: [stdio]
      env_from:
        GITHUB_PERSONAL_ACCESS_TOKEN: GITHUB_TOKEN   # server var: SignalBox env var
      tools: [search_issues, get_issue]
    docs:
      url: https://docs.example.com/mcp              # Streamable HTTP endpoint
      headers_from:
        Authorization: DOCS_MCP_AUTH                 # full header value in environment
      tools: [search]
```

For HTTP servers use `url` instead of `command`, `args` and `env_from`.
`headers_from` is optional and maps HTTP header names to environment
variables containing the complete header values (for example,
`DOCS_MCP_AUTH` could contain `Bearer <token>`). SignalBox reads them when
the agent is invoked; missing variables fail the call. URLs must use HTTP or
HTTPS and cannot contain credentials or fragments. Use HTTPS for remote
servers and do not put secrets in URLs or query strings.

Then each agent names the servers (and optionally a subset of tools) it needs:

```yaml
agents:
  - id: triager
    runtime: claude
    mcp:
      - github                                  # all tools allowed above
      - server: docs
        tools: [search]
```

- For agent tool access, SignalBox does not host external MCP servers. For
  each call it passes only the granted servers and tools to the agent CLI:
  - **codex**: `-c mcp_servers.<name>.*` overrides with `enabled_tools`.
  - **claude**: `--mcp-config <file> --strict-mcp-config --allowedTools
    mcp__<name>__<tool>,...`.
  - **copilot**: `--additional-mcp-config @<file> --allow-tool
    '<name>(<tool>)',...`.
- Validation fails if MCP is not enabled, a server is not registered, a tool
  is not in the server's `tools`, the runtime is not codex/claude/copilot, or
  a wildcard is used. Session participants can use MCP; orchestrators cannot.
- **Security:**
  - Local MCP servers run as the user running SignalBox, with that user's
    access. HTTP servers receive the configured headers and tool requests.
  - Keep secrets out of YAML: `env_from` and `headers_from` name environment
    variables that SignalBox reads at call time.
  - codex receives secret values through its environment (`env_vars` or
    `env_http_headers`).
    claude and copilot receive them in a temporary JSON file, readable only
    by the current user and deleted after the call.
  - Runs record which servers and tools were granted, never environment
    values.
  - Only claude is told to ignore other MCP configuration
    (`--strict-mcp-config`). codex and copilot still load the user's own
    global MCP servers alongside the granted ones.

Demo: `repo-explorer-mcp`, using the read-only filesystem server registered
in `testproject/signalbox.yaml` (requires Node.js for `npx`).

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
