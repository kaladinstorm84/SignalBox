# SignalBox MCP server

[Back to SignalBox](../README.md)

## MCP server (`signalbox mcp`)

SignalBox can expose its running HTTP API to an AI client over MCP stdio.
This is separate from granting external MCP tools to pipeline agents: the
client controls SignalBox, while SignalBox continues to execute workflows.
The adapter does not start a second engine or own the lifetime of executions.

Start `signalbox serve` normally, then configure your MCP client to launch:

```text
signalbox mcp --url http://127.0.0.1:8484
```

Without `--url`, the command reads the server address from `signalbox.yaml`
in its working directory. An explicit URL lets the client run from anywhere.
The target must be an HTTP(S) origin, without credentials, paths, query
parameters or fragments. The API must already be running; connection failures
are reported on stderr. Stdout is reserved for MCP messages.

For example, a VS Code `.vscode/mcp.json` configuration (with `signalbox` on
`PATH`) is:

```json
{
  "servers": {
    "signalbox": {
      "type": "stdio",
      "command": "signalbox",
      "args": ["mcp", "--url", "http://127.0.0.1:8484"]
    }
  }
}
```

### Command tools

The tool list is stable: pipelines are resources, not dynamically generated
tools. Tools use the existing API's validation, child-execution routing,
recovery checks and error responses.

| Tool | Purpose |
|------|---------|
| `start_execution` | Start a named pipeline with `input` text, optional string-valued `params`, and optional `files` (`name`, `content_base64`). For schema-validated input, pass JSON encoded as text. |
| `cancel_execution` | Cancel a running execution. |
| `retry_run` | Retry a failed call awaiting retry, using `execution_id` and `run_id`. |
| `restart_execution` | Start a recovered execution with optional `run_id`, `node_id`, `stage`, and `force`. Definition-change conflicts are preserved; never automatically force them. |
| `rerun_execution` | Create another execution using the original input, parameters and files. |
| `add_chunk` | Add a work item with `execution_id` and `title`. |
| `complete_chunk` | Mark a work item done with `execution_id` and `chunk_id`. |
| `remove_chunk` | Remove a work item with `execution_id` and `chunk_id`. |
| `resolve_approval` | Resolve a human approval with `execution_id`, `run_id`, `approved`, and optional `comment`; exposed only with `--allow-review`. |
| `submit_manual_edit` | Submit replacement text with `execution_id`, `run_id`, and `output`; exposed only with `--allow-review`. |

Tools publish input/output schemas and side-effect annotations. Successful
results include structured `execution_id`, `resource_uri`, `dashboard_url`
and the original API `response`, plus a resource link and a text representation
for compatibility. Creation acknowledges an execution, not completion.
Repeated starts/reruns/restarts create separate executions: there is no
idempotency key and the adapter does not retry ambiguous failures.

Attachments are supplied as contents, never local filesystem paths. Their
combined decoded size is limited to 8 MiB; the server's own upload count,
size, extension and pipeline rules still apply.

### Resources and templates

| URI | Content |
|-----|---------|
| `signalbox://meta` | Project name and upload limits. |
| `signalbox://pipelines` | Pipeline catalog with descriptions, parameters, input schema references and workflow nodes. |
| `signalbox://pipelines/{name}` | One pipeline's API projection. Individual pipelines also appear in resource discovery. |
| `signalbox://schemas` | Schema catalog, usage references and load errors. |
| `signalbox://schemas/{name}` | One schema document and its usage references. |
| `signalbox://executions{?page,page_size}` | Execution summaries; the API's pagination rules apply (default page 1, size 20). |
| `signalbox://executions/{id}` | Full API execution detail, including pending gates, child links and surfaced runs. |
| `signalbox://executions/{id}/results{?offset,limit}` | Final outputs, execution status, terminal flag and per-output resource links. |
| `signalbox://executions/{id}/approvals{?offset,limit}` | Pending human approval gates, including surfaced child gates, with review input, owning execution/pipeline/run/node IDs and execution resource links. |
| `signalbox://executions/{id}/runs/{run_id}/output{?offset,limit}` | A node run's output in Unicode-character pages (default limit 8192; maximum 65536). |
| `signalbox://executions/{id}/runs/{run_id}/transcript{?offset,limit}` | Session turns, round, votes and consensus outcome. |
| `signalbox://executions/{id}/chunks` | Current work items. |
| `signalbox://executions/{id}/files/{name}` | Attached file contents as a binary resource. |

Results, approvals and transcript pages default to 20 items, maximum 100. Paged derived
resources use zero-based `offset` and return `total` and `next_offset` (`null`
at the end). Resource contents are limited to 1 MiB and upstream responses
to 16 MiB. Oversized reads fail explicitly rather than silently truncating:
use smaller pages, paged run output, or the HTTP API/dashboard. In particular,
a single oversized transcript turn or execution record may require HTTP access.

The adapter polls the pipeline catalog and subscribed resources every two
seconds. It sends resource-list change notifications for pipeline discovery
changes and resource-update notifications for changed subscriptions,
including resources that become unavailable. Notifications are hints to
reread persisted state, not a durable event log or a promise to wake a model.
Disconnected executions continue running. Clients must support resource
discovery/reads for this resource-first interface.

### Watching approvals

Clients can subscribe to `signalbox://executions/{id}/approvals` without
`--allow-review`. Subscribe first, then read the current resource so the
client has an initial view even if nothing changes. For example, on MCP
protocol versions using `resources/subscribe`, the request parameters are:

```json
{
  "method": "resources/subscribe",
  "params": {
    "uri": "signalbox://executions/abc123/approvals"
  }
}
```

The adapter watches that URI automatically while connected. When an approval
appears, changes, or is resolved (including surfaced child approvals), it
sends a `notifications/resources/updated` notification identifying the URI.
The notification does not contain the approval payload: use `resources/read`
with that URI to retrieve the latest pending gates. Prefer subscribing to
the unpaged URI to track the pending count and first page; read additional
pages as needed.

Use the MCP client's subscription API where available: the SDK negotiates
the subscription mechanism for the client's protocol version. Unsubscribe
when no longer interested, and resubscribe and reread after reconnecting.
Updates are detected by the two-second polling loop, not sent as a durable
stream of every intermediate transition. Reading or subscribing never
resolves a gate or requires delegated review permission.

### Permissions and scope

Approval and manual-edit tools are **not exposed by default**. Reading or
subscribing to the approvals resource does not require
`--allow-review` and cannot resolve a gate. Resolved approvals disappear from
this pending-only view; historical decisions remain in execution detail.
Manual-edit and automated LLM gates are not included.

Explicitly launching with `--allow-review` adds `resolve_approval` (`execution_id`,
`run_id`, `approved`, optional `comment`) and `submit_manual_edit`
(`execution_id`, `run_id`, `output`). Review and chunk mutations record the
actor as `mcp-client`, not a human identity. Only enable delegated review
when your workflow policy permits it; otherwise use the dashboard.

This flag controls tool exposure, not HTTP authentication. The adapter has
the target API's authority and exposes all its pipelines and executions:
use it only with trusted clients and a trusted server, normally on loopback.
Pipeline starts can execute local code, consume model budget and use external
services. Do not automatically grant this server back to pipeline agents,
which could otherwise recursively launch workflows.

Settings, pipeline-file editing, registry administration and execution deletion
are not exposed. Prompts, elicitation, sampling and experimental MCP tasks are
not part of this interface.
