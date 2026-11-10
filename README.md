# SignalBox

SignalBox is a YAML based AI agent workflow system with human approval stages.
You describe a pipeline of agents in YAML, point each agent at a runtime
(Codex, Claude, Copilot, or any OpenAI-compatible API), and SignalBox executes
the chain, pausing at approval gates for review in the web UI or terminal.
Pipelines can also execute local code, send rejected work back for revision,
and start automatically from command-based watchers.

Built in Go with the web UI embedded in the executable. External agent CLIs
and script interpreters are installed separately when needed.

## Get started

- [Install SignalBox, configure PATH, and update](help/installation.md)
- [Download Windows x64 releases](https://github.com/kaladinstorm84/SignalBox/releases)
- [Use the web UI](help/web-ui.md) or [command line](help/command-line.md)

The starter pipeline uses a mock runtime, so you can try it without AI credentials.

```powershell
go install github.com/kaladinstorm84/SignalBox/cmd/signalbox@latest
signalbox init my-project
Set-Location my-project
signalbox serve
```

Open `http://127.0.0.1:8484`. See the installation guide if `signalbox` is not
on your PATH, or to install without Go.

## Help

### Build workflows

| Guide | Covers |
|-------|--------|
| [Pipelines and agent types](help/pipelines.md) | YAML examples, handoffs, human approvals and LLM quality gates |
| [Parameters, imports and templates](help/parameters-and-templates.md) | Reusable and configurable workflows |
| [Pipeline chaining](help/pipeline-chaining.md) | Child pipelines and reusable sections |
| [Runtimes, models and MCP access](help/runtimes.md) | AI backends, model overrides and agent tool access |
| [Knowledge bases](help/knowledge-bases.md) | Documents attached to agent context |
| [Structured responses and schemas](help/structured-output.md) | JSON validation, repair and field routing |
| [File uploads](help/files.md) | Attachments, delivery modes and limits |
| [Sessions and chunks](help/sessions.md) | Multi-agent discussion and consensus |
| [Code steps](help/code-steps.md) | Deterministic local scripts |
| [OpenSpec integration](help/openspec.md) | Specification-driven command nodes and approval-gated example |

### Run and manage

| Guide | Covers |
|-------|--------|
| [Installation and updates](help/installation.md) | Go install, Windows PATH, release ZIPs and troubleshooting |
| [Web UI and execution lifecycle](help/web-ui.md) | Pages, diagrams and detached workers |
| [Command line](help/command-line.md) | Launch, approve, reject and manage executions |
| [Retries, context and recovery](help/execution.md) | Failure handling, context strategies and restart |
| [Project configuration and watchers](help/configuration.md) | Project settings and automatic triggers |
| [Pipeline and knowledge registry](help/registry.md) | Publish, install and track versioned packages |

### Integrate and develop

- [SignalBox MCP server](help/mcp-server.md)
- [HTTP API](help/http-api.md)
- [Development](help/development.md)
- [Publishing releases](help/releasing.md)

## Examples

Explore the [sample project](testproject/) for runnable pipelines, schemas,
scripts, knowledge bases and registry packages.

## License

See [LICENSE](LICENSE).
