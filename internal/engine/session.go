package engine

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/kaladinstorm84/SignalBox/internal/pipeline"
	"github.com/kaladinstorm84/SignalBox/internal/runtime"
)

const (
	moderatorConsensusLine   = "CONSENSUS: REACHED"
	maxDirectivesPerTurn     = 20
	chunkDirectiveAdd        = "CHUNK_ADD:"
	chunkDirectiveDone       = "CHUNK_DONE:"
	chunkDirectiveRemove     = "CHUNK_REMOVE:"
	sessionChunkSourcePrefix = "session:"
)

// runSession drives a multi-round discussion between participant agents.
// Each turn is persisted as it completes; turns already in the transcript
// (from an interrupted run) are skipped, so a restarted session resumes.
func (eng *Engine) runSession(le *liveExec, run *NodeRun, node *pipeline.Node, octx *stageState) {
	eng.mu.Lock()
	if run.Session == nil {
		run.Session = &SessionRun{}
	}
	run.Session.MaxRounds = node.MaxRounds
	if node.ChunksEnabled() && !run.Session.InitialChunks {
		for _, title := range node.Chunks.Initial {
			addChunkLocked(le.exec, title, sessionChunkSourcePrefix+node.ID)
		}
		run.Session.InitialChunks = true
	}
	eng.persistLocked(le)
	eng.mu.Unlock()

	order := node.TurnSequence()
	consensusRound := 0
	for round := 1; round <= node.MaxRounds && consensusRound == 0; round++ {
		for _, participantID := range order {
			eng.mu.Lock()
			done := hasTurn(run.Session, round, participantID, false)
			eng.mu.Unlock()
			if done {
				continue
			}
			if err := eng.sessionTurn(le, run, node, round, participantID, false); err != nil {
				eng.finishRunErr(le, run, node, octx, err)
				return
			}
		}
		eng.mu.Lock()
		if sessionAgreed(node, run.Session, le.exec.Chunks, round) {
			consensusRound = round
		}
		eng.mu.Unlock()
	}

	outcome := OutcomeNoConsensus
	if consensusRound > 0 {
		outcome = OutcomeConsensus
	}
	if outcome == OutcomeNoConsensus && !hasNoConsensusRoute(node) {
		eng.finishSessionRejected(le, run, node, octx)
		return
	}

	var output string
	if node.SummaryAgent != "" {
		eng.mu.Lock()
		summarized := hasTurn(run.Session, 0, node.SummaryAgent, true)
		eng.mu.Unlock()
		if !summarized {
			if err := eng.sessionTurn(le, run, node, 0, node.SummaryAgent, true); err != nil {
				eng.finishRunErr(le, run, node, octx, err)
				return
			}
		}
		eng.mu.Lock()
		for _, turn := range run.Session.Turns {
			if turn.Summary {
				output = turn.Output
			}
		}
		eng.mu.Unlock()
	} else {
		eng.mu.Lock()
		output = sessionMinutes(node, run.Session, le.exec.Chunks)
		eng.mu.Unlock()
	}

	eng.mu.Lock()
	run.Session.Outcome = outcome
	run.Session.ConsensusRound = consensusRound
	run.Decision = outcome
	run.DecisionSource = "session"
	eng.mu.Unlock()
	eng.finishRunOK(le, run, node, output, octx, outcome)
}

// sessionTurn invokes one participant (or the summary agent when summary is
// true) and records the turn, applying any chunk directives it contains.
func (eng *Engine) sessionTurn(le *liveExec, run *NodeRun, node *pipeline.Node, round int, participantID string, summary bool) error {
	if err := le.ctx.Err(); err != nil {
		return err
	}
	participant := le.p.Node(participantID)
	if participant == nil {
		return fmt.Errorf("session %q: participant %q is no longer in the pipeline", node.ID, participantID)
	}
	rt, err := eng.registry.Get(participant.Runtime)
	if err != nil {
		return err
	}

	eng.mu.Lock()
	run.Session.Round = round
	if summary {
		run.Session.Round = len(sessionRounds(run.Session))
	}
	req := runtime.Request{
		SystemPrompt: sessionSystemPrompt(node, participant, round, summary),
		Input:        sessionTurnInput(runtimeInput(run), run.Session, le.exec.Chunks, node.ChunksEnabled()),
		Model:        participant.Model,
		MCP:          agentGrants(run.MCP, participantID),
	}
	eng.persistLocked(le)
	eng.mu.Unlock()

	out, err := eng.invokeRetrying(le, run, node.Retry, func() (string, error) {
		return rt.Invoke(le.ctx, req)
	})
	if err != nil {
		return err
	}

	eng.mu.Lock()
	defer eng.mu.Unlock()
	turn := SessionTurn{
		Round:       round,
		Participant: participantID,
		Output:      out,
		Summary:     summary,
		At:          time.Now().UTC(),
	}
	if !summary {
		turn.Agreed = turnAgrees(node, participantID, out)
		if node.ChunksEnabled() {
			turn.Directives, turn.DirectiveErrors = applyChunkDirectives(le.exec, out, sessionChunkSourcePrefix+participantID)
		}
	}
	run.Session.Turns = append(run.Session.Turns, turn)
	run.Error = ""
	eng.persistLocked(le)
	return nil
}

// finishSessionRejected ends an execution whose session ran out of rounds
// without consensus and has no on_no_consensus route. It deliberately does
// not use the approval rework path: there is no earlier step to send back to.
func (eng *Engine) finishSessionRejected(le *liveExec, run *NodeRun, node *pipeline.Node, octx *stageState) {
	eng.mu.Lock()
	defer eng.mu.Unlock()
	delete(le.reworks, run.ID)
	now := time.Now().UTC()
	run.Status = StatusRejected
	run.FinishedAt = &now
	run.Session.Outcome = OutcomeNoConsensus
	run.Decision = OutcomeNoConsensus
	run.DecisionSource = "session"
	run.Output = sessionMinutes(node, run.Session, le.exec.Chunks)
	run.Comment = fmt.Sprintf("no consensus after %d rounds", node.MaxRounds)
	if !le.exec.Status.Terminal() {
		le.exec.Status = StatusRejected
		le.exec.Error = fmt.Sprintf("session %q reached max_rounds (%d) without consensus", node.ID, node.MaxRounds)
		if le.cancel != nil {
			le.cancel()
		}
	}
	eng.releaseStageLocked(octx)
	eng.endRunLocked(le)
	eng.persistLocked(le)
}

func hasNoConsensusRoute(node *pipeline.Node) bool {
	for _, route := range node.Handoff {
		if route.Condition == "on_no_consensus" || route.Condition == "always" {
			return true
		}
	}
	return false
}

func hasTurn(s *SessionRun, round int, participantID string, summary bool) bool {
	for _, turn := range s.Turns {
		if turn.Participant != participantID || turn.Summary != summary {
			continue
		}
		if summary || turn.Round == round {
			return true
		}
	}
	return false
}

// sessionRounds returns the distinct discussion rounds in transcript order.
func sessionRounds(s *SessionRun) []int {
	var rounds []int
	seen := map[int]bool{}
	for _, turn := range s.Turns {
		if !turn.Summary && !seen[turn.Round] {
			seen[turn.Round] = true
			rounds = append(rounds, turn.Round)
		}
	}
	return rounds
}

// sessionAgreed reports whether the given round reached consensus.
func sessionAgreed(node *pipeline.Node, s *SessionRun, chunks []Chunk, round int) bool {
	var agreed, total int
	decided := false
	for _, turn := range s.Turns {
		if turn.Round != round || turn.Summary {
			continue
		}
		total++
		if turn.Agreed {
			agreed++
			if turn.Participant == node.SummaryAgent {
				decided = true
			}
		}
	}
	reached := false
	switch node.Consensus.Method {
	case pipeline.ConsensusModeratorDecide:
		reached = decided
	default:
		participants := len(node.Participants)
		if total < participants {
			return false
		}
		if node.Consensus.Require == pipeline.ConsensusRequireMajority {
			reached = agreed*2 > participants
		} else {
			reached = agreed == participants
		}
	}
	if reached && node.Consensus.RequireChunksDone {
		for _, chunk := range chunks {
			if chunk.Status != ChunkDone {
				return false
			}
		}
	}
	return reached
}

// turnAgrees detects a consensus vote: a line that is exactly the keyword,
// ignoring case and light markdown emphasis.
func turnAgrees(node *pipeline.Node, participantID, output string) bool {
	keyword := node.Consensus.Keyword
	if node.Consensus.Method == pipeline.ConsensusModeratorDecide {
		if participantID != node.SummaryAgent {
			return false
		}
		keyword = moderatorConsensusLine
	}
	for _, line := range strings.Split(output, "\n") {
		if strings.EqualFold(normalizeDirectiveLine(line), keyword) {
			return true
		}
	}
	return false
}

func normalizeDirectiveLine(line string) string {
	return strings.TrimSpace(strings.Trim(strings.TrimSpace(line), "*_`"))
}

func sessionSystemPrompt(node *pipeline.Node, participant *pipeline.Node, round int, summary bool) string {
	var b strings.Builder
	b.WriteString(participant.Prompt)
	b.WriteString("\n\n--- session instructions ---\n")
	if summary {
		fmt.Fprintf(&b, "You are %q, the summary agent of session %q. The discussion has ended. "+
			"Write the final agreed outcome of the discussion from the transcript: decisions, "+
			"rationale, open questions, and the status of any chunks. This summary is the session's output.\n", participant.ID, node.ID)
		return b.String()
	}
	fmt.Fprintf(&b, "You are %q in a multi-agent session %q (participants: %s). This is round %d of at most %d. "+
		"Read the transcript, respond to the other participants, and move the discussion toward an agreed outcome.\n",
		participant.ID, node.ID, strings.Join(node.TurnSequence(), ", "), round, node.MaxRounds)
	if node.Consensus.Method == pipeline.ConsensusModeratorDecide {
		if participant.ID == node.SummaryAgent {
			fmt.Fprintf(&b, "You decide when the session is finished. When the participants have reached an agreed outcome, "+
				"write %s on its own line.\n", moderatorConsensusLine)
		}
	} else {
		fmt.Fprintf(&b, "When you agree with the current proposal and have no further changes, write %s on its own line. "+
			"Only vote if you genuinely agree.\n", node.Consensus.Keyword)
	}
	if node.ChunksEnabled() {
		b.WriteString("Track work items (chunks) with directives, each on its own line:\n" +
			"CHUNK_ADD: <title>\nCHUNK_DONE: <chunk id, e.g. C-001>\nCHUNK_REMOVE: <chunk id>\n")
		if node.Consensus.RequireChunksDone {
			b.WriteString("The session cannot finish until every chunk is done.\n")
		}
	}
	return b.String()
}

func sessionTurnInput(input string, s *SessionRun, chunks []Chunk, chunksEnabled bool) string {
	var b strings.Builder
	b.WriteString(input)
	if len(s.Turns) > 0 {
		b.WriteString("\n\n--- session transcript ---\n")
		for _, turn := range s.Turns {
			if turn.Summary {
				continue
			}
			fmt.Fprintf(&b, "\n[Round %d] %s:\n%s\n", turn.Round, turn.Participant, strings.TrimSpace(turn.Output))
		}
	}
	if chunksEnabled {
		b.WriteString("\n\n--- chunks ---\n")
		b.WriteString(formatChunks(chunks))
	}
	return b.String()
}

func formatChunks(chunks []Chunk) string {
	if len(chunks) == 0 {
		return "(no chunks)\n"
	}
	var b strings.Builder
	for _, chunk := range chunks {
		fmt.Fprintf(&b, "%s [%s] %s\n", chunk.ID, chunk.Status, chunk.Title)
	}
	return b.String()
}

// sessionMinutes renders the transcript as markdown, used as the session's
// output when it has no summary agent.
func sessionMinutes(node *pipeline.Node, s *SessionRun, chunks []Chunk) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Session %s\n", node.ID)
	for _, round := range sessionRounds(s) {
		fmt.Fprintf(&b, "\n## Round %d\n", round)
		for _, turn := range s.Turns {
			if turn.Summary || turn.Round != round {
				continue
			}
			mark := ""
			if turn.Agreed {
				mark = " (agreed)"
			}
			fmt.Fprintf(&b, "\n### %s%s\n\n%s\n", turn.Participant, mark, strings.TrimSpace(turn.Output))
		}
	}
	if node.ChunksEnabled() {
		b.WriteString("\n## Chunks\n\n")
		b.WriteString(formatChunks(chunks))
	}
	return b.String()
}

// applyChunkDirectives applies CHUNK_ADD/DONE/REMOVE lines from an agent's
// output. Invalid directives are reported, never fatal.
func applyChunkDirectives(exec *Execution, output, by string) (applied, problems []string) {
	count := 0
	for _, raw := range strings.Split(output, "\n") {
		line := normalizeDirectiveLine(raw)
		var verb, arg string
		for _, prefix := range []string{chunkDirectiveAdd, chunkDirectiveDone, chunkDirectiveRemove} {
			if len(line) >= len(prefix) && strings.EqualFold(line[:len(prefix)], prefix) {
				verb, arg = prefix, strings.TrimSpace(line[len(prefix):])
				break
			}
		}
		if verb == "" {
			continue
		}
		count++
		if count > maxDirectivesPerTurn {
			problems = append(problems, fmt.Sprintf("%s ignored: more than %d directives in one turn", line, maxDirectivesPerTurn))
			continue
		}
		var err error
		switch verb {
		case chunkDirectiveAdd:
			var chunk Chunk
			chunk, err = addChunkChecked(exec, arg, by)
			if err == nil {
				line = fmt.Sprintf("%s %s (%s)", chunkDirectiveAdd, chunk.Title, chunk.ID)
			}
		case chunkDirectiveDone:
			err = completeChunkLocked(exec, strings.ToUpper(arg), by)
		case chunkDirectiveRemove:
			err = removeChunkLocked(exec, strings.ToUpper(arg))
		}
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s: %v", line, err))
			continue
		}
		applied = append(applied, line)
	}
	return applied, problems
}

// --- chunk store (execution-scoped) ---

func addChunkChecked(exec *Execution, title, by string) (Chunk, error) {
	if err := pipeline.ValidateChunkTitle(title); err != nil {
		return Chunk{}, err
	}
	return addChunkLocked(exec, strings.TrimSpace(title), by), nil
}

func addChunkLocked(exec *Execution, title, by string) Chunk {
	exec.NextChunk++
	chunk := Chunk{
		ID:        fmt.Sprintf("C-%03d", exec.NextChunk),
		Title:     title,
		Status:    ChunkPending,
		CreatedBy: by,
		CreatedAt: time.Now().UTC(),
	}
	exec.Chunks = append(exec.Chunks, chunk)
	return chunk
}

func completeChunkLocked(exec *Execution, id, by string) error {
	for i := range exec.Chunks {
		if exec.Chunks[i].ID != id {
			continue
		}
		if exec.Chunks[i].Status == ChunkDone {
			return fmt.Errorf("chunk %s is already done", id)
		}
		now := time.Now().UTC()
		exec.Chunks[i].Status = ChunkDone
		exec.Chunks[i].DoneBy = by
		exec.Chunks[i].DoneAt = &now
		return nil
	}
	return fmt.Errorf("unknown chunk %q", id)
}

func removeChunkLocked(exec *Execution, id string) error {
	for i := range exec.Chunks {
		if exec.Chunks[i].ID == id {
			exec.Chunks = append(exec.Chunks[:i], exec.Chunks[i+1:]...)
			return nil
		}
	}
	return fmt.Errorf("unknown chunk %q", id)
}

// ListChunks returns a copy of an execution's chunks.
func (eng *Engine) ListChunks(execID string) ([]Chunk, error) {
	exec, err := eng.Get(execID)
	if err != nil {
		return nil, err
	}
	if exec.Chunks == nil {
		return []Chunk{}, nil
	}
	return exec.Chunks, nil
}

// AddChunk adds a chunk to a running execution.
func (eng *Engine) AddChunk(execID, title, by string) (Chunk, error) {
	var chunk Chunk
	err := eng.mutateChunks(execID, func(exec *Execution) error {
		var err error
		chunk, err = addChunkChecked(exec, title, chunkActor(by))
		return err
	})
	return chunk, err
}

// CompleteChunk marks a chunk of a running execution done.
func (eng *Engine) CompleteChunk(execID, chunkID, by string) error {
	return eng.mutateChunks(execID, func(exec *Execution) error {
		return completeChunkLocked(exec, strings.ToUpper(strings.TrimSpace(chunkID)), chunkActor(by))
	})
}

// RemoveChunk deletes a chunk from a running execution. Its ID is not reused.
func (eng *Engine) RemoveChunk(execID, chunkID string) error {
	return eng.mutateChunks(execID, func(exec *Execution) error {
		return removeChunkLocked(exec, strings.ToUpper(strings.TrimSpace(chunkID)))
	})
}

func chunkActor(by string) string {
	by = strings.TrimSpace(by)
	if by == "" {
		return "user"
	}
	return by
}

// ErrChunksReadOnly is returned when changing chunks of a finished execution.
var ErrChunksReadOnly = errors.New("chunks are read-only once an execution has finished")

func (eng *Engine) mutateChunks(execID string, mutate func(*Execution) error) error {
	eng.mu.Lock()
	defer eng.mu.Unlock()
	le, ok := eng.execs[execID]
	if !ok || le.exec.Status.Terminal() {
		if !ok && eng.store == nil {
			return fmt.Errorf("unknown execution %q", execID)
		}
		if !ok {
			if _, err := eng.store.Get(execID); err != nil {
				return err
			}
		}
		return fmt.Errorf("execution %q: %w", execID, ErrChunksReadOnly)
	}
	if err := mutate(le.exec); err != nil {
		return err
	}
	eng.persistLocked(le)
	return nil
}
