# Pipeline chaining

[Back to SignalBox](../README.md)

### Pipeline chaining

A `type: pipeline` node runs another loaded pipeline, looked up by its
`name`, as a linked **child execution**. Imports copy agent definitions into
a pipeline; chaining runs another pipeline as it stands, with its own
execution page.

```yaml
  - id: review_copy
    type: pipeline
    pipeline: content-line   # name of the pipeline to run
    from: draft              # optional: start here instead of its entry
    until: signoff           # optional: stop after this agent
    params:                  # optional: values for the child's params
      topic: ${{ params.subject }}
    retry: { count: 1, backoff: 2s }
    handoff: [collate]
```

- **Input and output.** The child gets the node's input (including any
  `additional-input`) as its run input. When it completes, its final output
  is handed off. If it has several final outputs, they are joined and
  labelled by agent.
- **Sections.** `from` and `until` must be top-level agents of the child
  (not ones reached only inside a stage or session), and `until` must be
  reachable from the start through handoffs.
  - `until` only stops that agent's handoffs. Parallel branches that bypass
    it still run, and their outputs are included.
  - Agents in the section cannot take `additional-input` from agents outside
    it.
- **Params.** `params` values may use the parent's own
  `${{ params.x }}`. Each key must be declared by the child, and the child's
  required params must be supplied.
- **Approvals surface on the parent.** Approvals, manual edits, and failed
  calls waiting in a child (or grandchild) are listed on the parent's page
  under **Waiting in linked pipelines**. You can handle them there, from
  `signalbox approve|reject|edit|retry <parent-id>`, or from the parent's
  API endpoints with the child run's `run_id`. Each run card for a pipeline
  node links to its child execution, and the child page links back to its
  parent.
- **Outcomes.**
  - **Completed child:** its output is handed off.
  - **Rejected child:** the node is rejected, and the usual rework rules
    apply in the parent. A rejection inside the child follows the child's
    own rules first; for example, rejecting an approval reworks the agent
    before it, so the child only ends rejected when nothing upstream remains.
  - **Failed or canceled child:** handled like a failed agent call. It is
    retried per the node's `retry` policy, then waits for a manual retry,
    which starts a fresh child execution. Earlier attempts stay linked.
  - **Canceled parent:** cancelling or failing the parent cancels its
    running children.
- **Restarts.** A child execution cannot be restarted or rerun on its own.
  Restart the parent from the pipeline node instead, which runs a new child.
  The parent's definition fingerprint does not include the child pipeline,
  so changes to the child do not trigger the changed-definition check.
- **Validation.** Errors are reported for:
  - unknown pipelines, `from`, or `until`;
  - undeclared or missing params;
  - reference cycles (for example `a -> b -> a`, including a pipeline that
    runs itself).

  Nesting is limited to 5 levels. Saving a pipeline in the editor is refused
  only for reference errors the save would introduce.
- **Registry.** Publishing a pipeline does not bundle the pipelines it
  references, so install those too.

Demos:
- `content-line`: the reusable line, with a param and a human sign-off.
- `chain-express`: runs all of `content-line` and passes params.
- `chain-section`: runs two sections of `content-line` in parallel, and
  surfaces the sign-off on the parent.
