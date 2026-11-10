# Sessions and chunks

[Back to SignalBox](../README.md)

### Sessions and chunks

A `session` node runs a conversation between **participants**: ordinary
`standard` agents that only the session invokes. Each round, every participant
takes one turn. Its prompt is its own `prompt` plus session instructions, and
its input is the session's input followed by the transcript so far (and the
chunk table, when chunks are enabled).

```yaml
- id: design_review
  type: session
  participants: [architect, security_reviewer, delivery_lead]
  turn_order: round_robin        # or moderator (moderator speaks first each round)
  # moderator: architect         # required with turn_order: moderator
  max_rounds: 4                  # 1–20, default 5
  summary_agent: delivery_lead   # optional; writes the session's output
  consensus:
    method: vote                 # vote (default) or moderator_decides
    keyword: "VOTE: AGREE"       # default; must appear on its own line
    require: majority            # all (default) or majority
    require_chunks_done: true    # optional; needs chunks.enabled
  chunks:
    enabled: true
    initial: [Agree the component boundaries]
  handoff:
    - to: sign_off
      condition: on_consensus
    - to: escalate
      condition: on_no_consensus
```

- **Consensus.** With `vote`, a participant agrees by writing the keyword on its
  own line (case-insensitive; `**bold**` is fine). A round reaches consensus
  when all, or more than half, of the participants agree in that round. With
  `moderator_decides`, only the `summary_agent` can end the session, by
  writing `CONSENSUS: REACHED`. With `require_chunks_done`, consensus also
  needs every chunk to be done; otherwise the discussion continues.
- **Output.** When the session ends, the `summary_agent` (if set) writes a
  final summary, which becomes the output. Without one, the output is
  Markdown minutes of the transcript.
- **Outcomes and routing.** A session ends with `consensus` or `no_consensus`.
  Handoffs with no condition, or with `on_consensus`, run on consensus;
  `on_no_consensus` routes run when the round limit is reached; `always` runs
  on either. If rounds run out and there is no `on_no_consensus` or `always`
  route, the execution is **rejected** with
  `session "<id>" reached max_rounds (N) without consensus`.
- **Participants** must be standard agents and belong to one session. They
  cannot be the entry, a handoff, `reject-to` or `additional-input` target, or
  a stage agent. They also cannot define their own `handoff`, `reject-to`,
  `additional-input` or `retry`; set `retry` on the session instead, which
  applies to each turn.
- **History and recovery.** Each turn is saved to the execution record (the
  run's `session` field) as soon as it completes, so transcripts survive
  restarts. **Retry from here** on an interrupted session resumes it in a new
  execution: completed turns are kept and not replayed, and chunks are copied.

**Chunks** are execution-scoped work items with stable IDs (`C-001`,
`C-002`, …). IDs are never reused, even after a chunk is removed. When a
session has `chunks.enabled`, participants manage chunks with directives,
each on its own line of their turn:

```
CHUNK_ADD: Write the migration plan
CHUNK_DONE: C-001
CHUNK_REMOVE: C-002
```

Titles must be one non-blank line of at most 200 characters, and each turn
can apply at most 20 directives. Invalid directives (an unknown ID, a blank
title, or an already-done chunk) are ignored and listed with the turn; they
do not fail the session. People can also manage chunks while the execution
is running, from the execution page, the API, or the CLI:

```bash
signalbox chunks list <execution>
signalbox chunks add <execution> Write the migration plan
signalbox chunks done <execution> C-001
signalbox chunks remove <execution> C-002
```

The CLI talks to the running server, using the address in `signalbox.yaml`
or `--addr host:port`. Use `--by name` to record who made the change. Chunks
become read-only once an execution finishes.

The test project includes two demos. `design-review` shows round-robin
voting with a majority, chunks with `require_chunks_done`, and
`on_consensus`/`on_no_consensus` routing. `incident-war-room` shows a
moderator with `moderator_decides`, where running out of rounds rejects the
execution.
