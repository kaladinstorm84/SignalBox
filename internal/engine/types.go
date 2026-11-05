package engine

import (
	"time"

	"github.com/kaladinstorm84/SignalBox/internal/pipeline"
)

// Status is the lifecycle state of an execution, node run, or stage.
type Status string

const (
	StatusPending           Status = "pending"
	StatusRunning           Status = "running"
	StatusWaitingApproval   Status = "waiting_approval"
	StatusWaitingManualEdit Status = "waiting_manual_edit"
	// StatusWaitingInputs applies to orchestrators still collecting fan-in
	// handoffs before their stages begin.
	StatusWaitingInputs Status = "waiting_inputs"
	// StatusWaitingRetry marks a run whose agent call failed and is paused
	// until a person retries it or gives up.
	StatusWaitingRetry Status = "waiting_retry"
	StatusCompleted    Status = "completed"
	StatusFailed       Status = "failed"
	StatusRejected     Status = "rejected"
	StatusCanceled     Status = "canceled"
	// StatusInterrupted marks runs that were in flight when the server stopped;
	// standard agent calls can be restarted in a new execution from saved input.
	StatusInterrupted Status = "interrupted"
)

// Terminal reports whether no further transitions can happen.
func (s Status) Terminal() bool {
	switch s {
	case StatusCompleted, StatusFailed, StatusRejected, StatusCanceled, StatusInterrupted:
		return true
	}
	return false
}

// StageOutput is one terminal chain result collected by an orchestrator stage.
type StageOutput struct {
	NodeRunID string `json:"node_run_id"`
	NodeID    string `json:"node_id"`
	Output    string `json:"output"`
}

// StageRun is the recorded execution of one orchestrator stage.
type StageRun struct {
	StageID string        `json:"stage_id"`
	Status  Status        `json:"status"`
	Outputs []StageOutput `json:"outputs,omitempty"`
	// Context is the synthesised result carried into the next stage.
	Context    string     `json:"context,omitempty"`
	StartedAt  *time.Time `json:"started_at,omitempty"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`

	// ownerRunID links back to the orchestrator NodeRun (not serialised).
	ownerRunID string
}

// NodeRun is one invocation of a pipeline node within an execution.
type NodeRun struct {
	ID                      string            `json:"id"`
	NodeID                  string            `json:"node_id"`
	Type                    pipeline.NodeType `json:"type"`
	Status                  Status            `json:"status"`
	Input                   string            `json:"input,omitempty"`
	Output                  string            `json:"output,omitempty"`
	Format                  bool              `json:"format,omitempty"`
	OutputHTML              string            `json:"output_html,omitempty"`
	AdditionalInputResolved bool              `json:"additional_input_resolved,omitempty"`
	Error                   string            `json:"error,omitempty"`
	// Retries counts how many times a failed agent call was retried.
	Retries int `json:"retries,omitempty"`

	// FromNodeID names the node that handed off to this run ("" for entry);
	// for a join it lists every source, comma-separated.
	FromNodeID string `json:"from_node_id,omitempty"`

	// OrchRunID / StageID identify the orchestrator stage this run belongs
	// to, when it was dispatched as part of one.
	OrchRunID string `json:"orch_run_id,omitempty"`
	StageID   string `json:"stage_id,omitempty"`

	// Stages is populated for orchestrator runs.
	Stages []*StageRun `json:"stages,omitempty"`

	// Human review metadata.
	ApprovedBy string `json:"approved_by,omitempty"`
	EditedBy   string `json:"edited_by,omitempty"`
	Comment    string `json:"comment,omitempty"`
	// DecisionSource and Decision distinguish automated gate verdicts from
	// human approvals in persisted history and the UI.
	DecisionSource string `json:"decision_source,omitempty"`
	Decision       string `json:"decision,omitempty"`
	GateReworks    int    `json:"gate_reworks,omitempty"`

	// Session is populated for session runs: rounds, turns, and outcome.
	Session *SessionRun `json:"session,omitempty"`

	// Context records the prior work selected by the node's context
	// strategy. It is assembled once, when the run starts, and reused by
	// retries and restarts.
	Context *ContextSelection `json:"context,omitempty"`

	StartedAt  *time.Time `json:"started_at,omitempty"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
}

// ContextSelection is the persisted result of applying a context strategy.
type ContextSelection struct {
	Strategy string `json:"strategy"`
	Window   int    `json:"window,omitempty"`
	MaxChars int    `json:"max_chars,omitempty"`
	// Available counts eligible earlier outputs before window and budget
	// limits; Skipped counts outputs omitted because the input already
	// contains them.
	Available int            `json:"available"`
	Skipped   int            `json:"skipped,omitempty"`
	Entries   []ContextEntry `json:"entries,omitempty"`
	// Chars is the number of output characters included across entries.
	Chars int    `json:"chars"`
	Text  string `json:"text,omitempty"`
}

// ContextEntry is one earlier output included in a run's context.
type ContextEntry struct {
	RunID         string `json:"run_id"`
	NodeID        string `json:"node_id"`
	Chars         int    `json:"chars"`
	OriginalChars int    `json:"original_chars"`
	Truncated     bool   `json:"truncated,omitempty"`
}

// Session outcomes, recorded in SessionRun.Outcome and NodeRun.Decision.
const (
	OutcomeConsensus   = "consensus"
	OutcomeNoConsensus = "no_consensus"
)

// SessionRun is the persisted state of a session node: its transcript is
// saved after every turn, so it survives reloads and supports resumption.
type SessionRun struct {
	MaxRounds int           `json:"max_rounds"`
	Round     int           `json:"round"`
	Turns     []SessionTurn `json:"turns,omitempty"`
	// Outcome is consensus or no_consensus once the session has finished.
	Outcome        string `json:"outcome,omitempty"`
	ConsensusRound int    `json:"consensus_round,omitempty"`
	InitialChunks  bool   `json:"initial_chunks_added,omitempty"`
}

// SessionTurn is one participant contribution, or the final summary.
type SessionTurn struct {
	Round       int    `json:"round"`
	Participant string `json:"participant"`
	Output      string `json:"output"`
	// Agreed marks a turn that voted for consensus (or, for
	// moderator_decides, declared it).
	Agreed  bool `json:"agreed,omitempty"`
	Summary bool `json:"summary,omitempty"`
	// Directives lists chunk directives applied from this turn; errors lists
	// directives that were rejected and ignored.
	Directives      []string  `json:"directives,omitempty"`
	DirectiveErrors []string  `json:"directive_errors,omitempty"`
	At              time.Time `json:"at"`
}

// Chunk statuses.
const (
	ChunkPending = "pending"
	ChunkDone    = "done"
)

// Chunk is a trackable unit of work within an execution. IDs are stable and
// never reused, even after a chunk is removed.
type Chunk struct {
	ID        string     `json:"id"`
	Title     string     `json:"title"`
	Status    string     `json:"status"`
	CreatedBy string     `json:"created_by,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
	DoneBy    string     `json:"done_by,omitempty"`
	DoneAt    *time.Time `json:"done_at,omitempty"`
}

// FinalOutput is the terminal output of a chain that was not owned by an
// orchestrator stage; these are the results of the execution.
type FinalOutput struct {
	NodeRunID string `json:"node_run_id"`
	NodeID    string `json:"node_id"`
	Output    string `json:"output"`
}

// Execution is a single run of a pipeline.
type Execution struct {
	ID           string        `json:"id"`
	Pipeline     string        `json:"pipeline"`
	Input        string        `json:"input"`
	Status       Status        `json:"status"`
	Error        string        `json:"error,omitempty"`
	Runs         []*NodeRun    `json:"runs"`
	FinalOutputs []FinalOutput `json:"final_outputs,omitempty"`
	// Chunks are execution-scoped work items managed by sessions and people.
	Chunks     []Chunk    `json:"chunks,omitempty"`
	NextChunk  int        `json:"next_chunk,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
}

// Summary is the list-view projection of an execution.
type Summary struct {
	ID          string     `json:"id"`
	Pipeline    string     `json:"pipeline"`
	Input       string     `json:"input"`
	Status      Status     `json:"status"`
	Waiting     int        `json:"waiting_approvals"`
	ManualEdits int        `json:"waiting_manual_edits"`
	Retrying    int        `json:"waiting_retries"`
	CreatedAt   time.Time  `json:"created_at"`
	FinishedAt  *time.Time `json:"finished_at,omitempty"`
}
