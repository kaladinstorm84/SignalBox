# Retries, context and recovery

[Back to SignalBox](../README.md)

### Runtime retries

Callable nodes can retry runtime errors automatically with a fixed delay:

```yaml
- id: researcher
  runtime: codex
  retry:
    count: 2
    backoff: 500ms
```

`count` is the number of retries after the initial call (0–10), and `backoff`
is a Go duration from 0 to 5 minutes. Cancellation interrupts the delay.
After automatic retries are exhausted, the existing `waiting_retry` action
remains available to retry the call manually or cancel the execution. Runtime
errors are separate from quality-gate rejection and code-node rejection
outcomes; automatic retries do not treat those decisions as transient failures.

The test project's `discovery` pipeline demonstrates conditional handoffs,
payload templates, an `llm_gate` with bounded rework, and runtime retries. The
`Full Pipeline QA` pipeline puts an `llm_gate` after each of ten specialist
planners, retaining two bounded reworks per planner. Local templates share
runtime/retry settings, and planners plus category judges use the
`qa-test-plan` schema with bounded repair. Tests include requirement references,
actionable steps and P0-P3 priorities; empty test lists require a justified
exclusion. Schemas check structure, while gates check relevance and coverage
against the requirements summary. Gates pass that requirements context through
to category judges, so judges do not append another copy.

The master judge consolidates a draft for a Three Amigos session: Business,
Developer, then QA take round-robin turns for at most two rounds, requiring
unanimous agreement. They review concise findings and corrections by test/REQ
ID rather than repeatedly rewriting the plan. QA summarizes agreed changes,
unagreed proposals and dissent; a final presenter receives both the draft and
review findings, preserves full test details, and applies agreed corrections.
Both consensus and no-consensus routes reach human approval, with the latter
explicitly marked as requiring human resolution. Model agreement is not a
substitute for human approval.

Human rejection sends the latest reviewed plan and feedback to a revision
agent, then repeats the Three Amigos session and presentation without rerunning
specialist planning or the original consolidation. Revisit upstream planning
separately when feedback requires new specialist analysis. A successful first
pass uses 32 LLM calls for one round, or 35 for two, excluding retries, schema
repairs and quality reworks. Jira retrieval still
requires suitable tools in the configured runtime; a prompt alone does not
provide a Jira integration.

### Context strategies

By default a node receives only its handoff input (plus any
`additional-input`). A `context` block appends selected **earlier outputs from
the same execution** to the text sent to the runtime:

```yaml
- id: editor
  type: llm_gate
  runtime: codex
  context:
    strategy: all        # none | all | recent | own
    max_chars: 12000     # optional budget; 0 or omitted means no limit
- id: analyst
  runtime: codex
  context:
    strategy: recent
    window: 2            # recent: default 1; own: default 0 (every earlier output)
```

| Strategy | Context contains |
|----------|------------------|
| `none` (default) | Nothing. The input is exactly as before. |
| `all` | Every eligible earlier output, oldest first. `window` is not accepted. |
| `recent` | The latest `window` eligible outputs (default 1). |
| `own` | This node's own earlier outputs in the execution, for example drafts that a gate rejected (latest `window` if set). |

Exact rules:

- **Eligible outputs** come from completed `standard`, `code`, `session`,
  `orchestrator`, and `manual_edit` runs with non-empty output. Approval and
  `llm_gate` runs are excluded, because they pass their input through
  unchanged. Failed, running, and canceled runs are excluded.
- Outputs are ordered by completion time. An output that already appears
  verbatim in the node's input (its handoff payload, a joined input, or an
  additional input) is **skipped**, so context never repeats what the node
  already receives. `window` applies after skipping.
- **`max_chars`** counts characters (Unicode code points) of the included
  outputs, not the headers. The budget is spent newest-first: newer outputs
  are kept whole, at most one older output is truncated (its beginning is
  kept), and anything older is dropped.
- The context is appended after the input in this format:

  ```
  <input>

  --- context (recent): 2 of 4 earlier outputs ---

  --- context from researcher (truncated to 1200 of 5000 characters) ---
  <first 1200 characters>
  [... 3800 characters truncated]

  --- context from writer ---
  <output>
  ```

  "2 of 4" means two of the four eligible outputs left after skipping. If
  nothing is selected, nothing is appended.
- Context is assembled once, when the run starts, and saved with the run
  (strategy, the included entries with their character counts, and the
  text). Manual retries and **Retry from here** reuse the saved context.
  Context is never part of the run's saved input, so gates and approvals
  pass their original payload downstream.
- `context` is supported on `standard`, `code` (the context is appended to
  stdin), `llm_gate`, and `session` nodes. On a session, it is added to every
  turn's input. Session participants cannot set it; set it on the session.
  `additional-input` is unchanged and works alongside context.

SignalBox makes no token-savings claims for these strategies. The
execution view shows how many characters each run's context added, so you
can measure the effect on your own pipelines. The `context-strategies` demo
pipeline uses all four strategies, `window`, and `max_chars`.

## Recovery and restart

A finished execution (failed, rejected, canceled, interrupted, or completed)
can be restarted from any of its top-level runs, or from a stage of an
orchestrator run. Restarting creates a **new** execution; the original is left
untouched and linked from the new one.

- The selected node runs again with its saved input, context selection, and
  chunk state. Everything downstream of it runs again as it is reached.
- Completed runs that are not downstream of the restart point are copied into
  the new execution and marked **reused** (with a link to where they ran)
  instead of being repeated. Their handoffs into joins that will run again are
  replayed, so a fan-in agent still receives every branch.
- Restarting an orchestrator from a stage keeps the earlier stages' results
  and the context they saved; every earlier stage must have completed. A run
  inside a stage cannot be a restart point on its own; restart its
  orchestrator from that stage.
- An interrupted session resumes its transcript and chunk list.
- With no run given, the most recent failed, interrupted, or canceled run is
  chosen (for a rejected execution, the rejecting run).

Each execution stores a fingerprint of the resolved pipeline. If the
definition has changed since the source ran, the restart is **refused** and
the changes (a changed entry, or added, removed, or modified agents; agents
are compared after parameter substitution) are
listed. Confirm in the UI or pass `--force` to restart on the current
definition; the changes are recorded as warnings on the new execution. A
restart point whose agent was removed or changed type cannot be forced.
Changes to script files used by code steps are not detected. Executions from
before fingerprints existed restart with a warning.

Limitations: there is one restart point per restart. Unfinished runs that
are not downstream of it (for example, a parallel branch that was canceled
alongside the failure) are not rerun; this is recorded as a warning. Restart
snapshots are stored with the execution, so they are removed with it by
`execution_retention`.

Try it with `pipelines/recovery-drill.yaml` in the test project: its verify
stage fails until `recovery-drill.fixed` exists in the project root. Create
the file and use **Restart from this stage** on `verify`, or
`signalbox restart <execution> --node coordinator --stage verify`. The new
execution reuses intake, both scouts, merge, and the draft stage.

Behaviour changes from earlier versions: **Retry from here** on an interrupted
run now carries completed runs into the new execution and replays sibling
inputs into joins, and it is refused when the pipeline changed unless forced.
