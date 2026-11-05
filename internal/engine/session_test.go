package engine

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kaladinstorm84/SignalBox/internal/config"
	"github.com/kaladinstorm84/SignalBox/internal/pipeline"
	"github.com/kaladinstorm84/SignalBox/internal/runtime"
)

var roundPattern = regexp.MustCompile(`This is round (\d+) of`)

func promptRound(req runtime.Request) int {
	m := roundPattern.FindStringSubmatch(req.SystemPrompt)
	if m == nil {
		return 0
	}
	n, _ := strconv.Atoi(m[1])
	return n
}

func isSummaryCall(req runtime.Request) bool {
	return strings.Contains(req.SystemPrompt, "The discussion has ended")
}

// voter agrees from the given round onwards.
func voter(name string, fromRound int) func(context.Context, runtime.Request) (string, error) {
	return func(_ context.Context, req runtime.Request) (string, error) {
		if isSummaryCall(req) {
			return "summary by " + name, nil
		}
		r := promptRound(req)
		if fromRound > 0 && r >= fromRound {
			return fmt.Sprintf("%s round %d: looks good\n**VOTE: AGREE**", name, r), nil
		}
		return fmt.Sprintf("%s round %d: more changes needed", name, r), nil
	}
}

func sessionPipeline(session string, extra string) string {
	carol := ""
	if strings.Contains(session, "carol") {
		carol = `
  - id: carol
    runtime: carol
    prompt: You are Carol.`
	}
	return `
name: p
entry: start
agents:
  - id: start
    runtime: mock
    handoff: [talk]
  - id: talk
    type: session
` + session + `
  - id: alice
    runtime: alice
    prompt: You are Alice.
  - id: bob
    runtime: bob
    prompt: You are Bob.` + carol + "\n" + extra
}

func TestSessionReachesConsensusAndRoutesSummary(t *testing.T) {
	var nextInput string
	eng := newTestEngine(t, sessionPipeline(`
    participants: [alice, bob, carol]
    summary_agent: carol
    max_rounds: 4
    handoff:
      - to: next
        condition: on_consensus
      - to: fallback
        condition: on_no_consensus
`, `
  - id: next
    runtime: next
  - id: fallback
    runtime: fallback
`), map[string]func(context.Context, runtime.Request) (string, error){
		"alice": voter("alice", 1),
		"bob":   voter("bob", 2),
		"carol": voter("carol", 1),
		"next": func(_ context.Context, req runtime.Request) (string, error) {
			nextInput = req.Input
			return "next done", nil
		},
		"fallback": func(context.Context, runtime.Request) (string, error) {
			t.Error("fallback should not run on consensus")
			return "", nil
		},
	})
	id, err := eng.Start("p", "design the thing")
	if err != nil {
		t.Fatal(err)
	}
	exec := waitDone(t, eng, id)
	if exec.Status != StatusCompleted {
		t.Fatalf("status = %s (%s)", exec.Status, exec.Error)
	}
	run := findRun(t, exec, "talk")
	s := run.Session
	if s == nil || s.Outcome != OutcomeConsensus || s.ConsensusRound != 2 {
		t.Fatalf("session = %+v, want consensus in round 2", s)
	}
	if len(s.Turns) != 7 || !s.Turns[6].Summary || s.Turns[6].Participant != "carol" {
		t.Fatalf("turns = %+v, want 6 discussion turns and a carol summary", s.Turns)
	}
	if s.Turns[1].Agreed || !s.Turns[4].Agreed {
		t.Fatalf("bob votes: round1=%v round2=%v", s.Turns[1].Agreed, s.Turns[4].Agreed)
	}
	if run.Output != "summary by carol" || nextInput != "summary by carol" {
		t.Fatalf("output %q, next input %q; want summary", run.Output, nextInput)
	}
	if run.Decision != OutcomeConsensus || run.DecisionSource != "session" {
		t.Fatalf("decision = %q/%q", run.Decision, run.DecisionSource)
	}
}

func TestSessionTranscriptReachesLaterTurns(t *testing.T) {
	var bobInput, bobPrompt string
	eng := newTestEngine(t, sessionPipelineTwo(), map[string]func(context.Context, runtime.Request) (string, error){
		"alice": func(context.Context, runtime.Request) (string, error) { return "alice proposes X", nil },
		"bob": func(_ context.Context, req runtime.Request) (string, error) {
			bobInput, bobPrompt = req.Input, req.SystemPrompt
			return "bob agrees\nVOTE: AGREE", nil
		},
	})
	id, err := eng.Start("p", "topic")
	if err != nil {
		t.Fatal(err)
	}
	exec := waitDone(t, eng, id)
	if exec.Status != StatusCompleted {
		t.Fatalf("status = %s (%s)", exec.Status, exec.Error)
	}
	if !strings.Contains(bobInput, "topic") || !strings.Contains(bobInput, "[Round 1] alice:\nalice proposes X") {
		t.Fatalf("bob input missing session input or transcript: %q", bobInput)
	}
	if !strings.HasPrefix(bobPrompt, "You are Bob.") || !strings.Contains(bobPrompt, "VOTE: AGREE") {
		t.Fatalf("bob prompt = %q", bobPrompt)
	}
	run := findRun(t, exec, "talk")
	// No summary agent: the output is the minutes, and always routes on no consensus.
	if run.Session.Outcome != OutcomeNoConsensus || !strings.Contains(run.Output, "## Round 1") || !strings.Contains(run.Output, "### bob (agreed)") {
		t.Fatalf("outcome %q output %q", run.Session.Outcome, run.Output)
	}
	if findRun(t, exec, "next").Status != StatusCompleted {
		t.Fatal("always route did not run")
	}
}

func sessionPipelineTwo() string {
	return `
name: p
entry: start
agents:
  - id: start
    runtime: mock
    handoff: [talk]
  - id: talk
    type: session
    participants: [alice, bob]
    max_rounds: 1
    handoff:
      - to: next
        condition: always
  - id: alice
    runtime: alice
    prompt: You are Alice.
  - id: bob
    runtime: bob
    prompt: You are Bob.
  - id: next
    runtime: mock
`
}

func TestSessionMajorityConsensus(t *testing.T) {
	eng := newTestEngine(t, sessionPipeline(`
    participants: [alice, bob, carol]
    consensus:
      require: majority
`, ""), map[string]func(context.Context, runtime.Request) (string, error){
		"alice": voter("alice", 1),
		"bob":   voter("bob", 1),
		"carol": voter("carol", 0),
	})
	id, _ := eng.Start("p", "x")
	exec := waitDone(t, eng, id)
	s := findRun(t, exec, "talk").Session
	if exec.Status != StatusCompleted || s.ConsensusRound != 1 || len(s.Turns) != 3 {
		t.Fatalf("status %s, session %+v", exec.Status, s)
	}
}

func TestSessionNoConsensusRoutesOrRejects(t *testing.T) {
	handlers := func() map[string]func(context.Context, runtime.Request) (string, error) {
		return map[string]func(context.Context, runtime.Request) (string, error){
			"alice": voter("alice", 0),
			"bob":   voter("bob", 1),
			"carol": voter("carol", 1),
		}
	}

	t.Run("on_no_consensus", func(t *testing.T) {
		var fallbackRan bool
		h := handlers()
		h["fallback"] = func(context.Context, runtime.Request) (string, error) {
			fallbackRan = true
			return "escalated", nil
		}
		h["next"] = func(context.Context, runtime.Request) (string, error) {
			t.Error("default route should not run without consensus")
			return "", nil
		}
		eng := newTestEngine(t, sessionPipeline(`
    participants: [alice, bob, carol]
    max_rounds: 2
    handoff:
      - next
      - to: fallback
        condition: on_no_consensus
`, `
  - id: next
    runtime: next
  - id: fallback
    runtime: fallback
`), h)
		id, _ := eng.Start("p", "x")
		exec := waitDone(t, eng, id)
		run := findRun(t, exec, "talk")
		if exec.Status != StatusCompleted || !fallbackRan || run.Decision != OutcomeNoConsensus || len(run.Session.Turns) != 6 {
			t.Fatalf("status %s fallback %v decision %q turns %d", exec.Status, fallbackRan, run.Decision, len(run.Session.Turns))
		}
	})

	t.Run("rejects without route", func(t *testing.T) {
		eng := newTestEngine(t, sessionPipeline(`
    participants: [alice, bob, carol]
    max_rounds: 2
`, ""), handlers())
		id, _ := eng.Start("p", "x")
		exec := waitDone(t, eng, id)
		run := findRun(t, exec, "talk")
		if exec.Status != StatusRejected || !strings.Contains(exec.Error, "max_rounds (2) without consensus") {
			t.Fatalf("status %s error %q", exec.Status, exec.Error)
		}
		if run.Status != StatusRejected || run.Session.Outcome != OutcomeNoConsensus || !strings.Contains(run.Output, "## Round 2") {
			t.Fatalf("run %+v", run)
		}
	})
}

func TestSessionModeratorDecides(t *testing.T) {
	var mu sync.Mutex
	var order []string
	record := func(name string, reply func(round int) string) func(context.Context, runtime.Request) (string, error) {
		return func(_ context.Context, req runtime.Request) (string, error) {
			mu.Lock()
			defer mu.Unlock()
			if isSummaryCall(req) {
				order = append(order, name+":summary")
				return "final decision", nil
			}
			order = append(order, name)
			return reply(promptRound(req)), nil
		}
	}
	eng := newTestEngine(t, sessionPipeline(`
    participants: [alice, bob, carol]
    turn_order: moderator
    moderator: carol
    summary_agent: carol
    consensus:
      method: moderator_decides
`, ""), map[string]func(context.Context, runtime.Request) (string, error){
		// Votes from non-moderators do not count under moderator_decides.
		"alice": record("alice", func(int) string { return "VOTE: AGREE" }),
		"bob":   record("bob", func(int) string { return "fine" }),
		"carol": record("carol", func(r int) string {
			if r == 2 {
				return "We are done.\nCONSENSUS: REACHED"
			}
			return "keep going"
		}),
	})
	id, _ := eng.Start("p", "x")
	exec := waitDone(t, eng, id)
	run := findRun(t, exec, "talk")
	want := "carol,alice,bob,carol,alice,bob,carol:summary"
	if got := strings.Join(order, ","); got != want {
		t.Fatalf("turn order = %s, want %s", got, want)
	}
	if exec.Status != StatusCompleted || run.Session.ConsensusRound != 2 || run.Output != "final decision" {
		t.Fatalf("status %s session %+v output %q", exec.Status, run.Session, run.Output)
	}
}

func TestSessionChunkDirectivesAndRequireChunksDone(t *testing.T) {
	eng := newTestEngine(t, sessionPipeline(`
    participants: [alice, bob]
    max_rounds: 3
    chunks:
      enabled: true
      initial: [Write spec]
    consensus:
      require_chunks_done: true
`, ""), map[string]func(context.Context, runtime.Request) (string, error){
		"alice": func(_ context.Context, req runtime.Request) (string, error) {
			switch promptRound(req) {
			case 1:
				if !strings.Contains(req.Input, "C-001 [pending] Write spec") {
					t.Errorf("alice did not see initial chunk: %q", req.Input)
				}
				return "CHUNK_ADD: Review API\nCHUNK_DONE: C-999\nVOTE: AGREE", nil
			case 2:
				return "- not a directive\nCHUNK_DONE: c-001\nCHUNK_DONE: C-002\nVOTE: AGREE", nil
			}
			return "VOTE: AGREE", nil
		},
		"bob": func(context.Context, runtime.Request) (string, error) { return "VOTE: AGREE", nil },
	})
	id, _ := eng.Start("p", "x")
	exec := waitDone(t, eng, id)
	run := findRun(t, exec, "talk")
	if exec.Status != StatusCompleted {
		t.Fatalf("status %s (%s)", exec.Status, exec.Error)
	}
	// Round 1 had unanimous votes but pending chunks, so it continued.
	if run.Session.ConsensusRound != 2 {
		t.Fatalf("consensus round = %d, want 2", run.Session.ConsensusRound)
	}
	first := run.Session.Turns[0]
	if len(first.Directives) != 1 || !strings.Contains(first.Directives[0], "(C-002)") || len(first.DirectiveErrors) != 1 || !strings.Contains(first.DirectiveErrors[0], "unknown chunk") {
		t.Fatalf("round 1 directives %q errors %q", first.Directives, first.DirectiveErrors)
	}
	if len(exec.Chunks) != 2 || exec.NextChunk != 2 {
		t.Fatalf("chunks = %+v", exec.Chunks)
	}
	for _, c := range exec.Chunks {
		if c.Status != ChunkDone || c.DoneBy != "session:alice" || c.DoneAt == nil {
			t.Fatalf("chunk not done by alice: %+v", c)
		}
	}
	if exec.Chunks[0].CreatedBy != "session:talk" || exec.Chunks[1].CreatedBy != "session:alice" {
		t.Fatalf("chunk sources: %+v", exec.Chunks)
	}
}

func TestChunkAPIOnRunningAndFinishedExecutions(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{}, 1)
	eng := newTestEngine(t, sessionPipeline(`
    participants: [alice, bob]
    max_rounds: 1
    chunks:
      enabled: true
    consensus:
      require_chunks_done: true
`, ""), map[string]func(context.Context, runtime.Request) (string, error){
		"alice": func(ctx context.Context, req runtime.Request) (string, error) {
			started <- struct{}{}
			select {
			case <-release:
			case <-ctx.Done():
				return "", ctx.Err()
			}
			return "VOTE: AGREE", nil
		},
		"bob": func(_ context.Context, req runtime.Request) (string, error) {
			if !strings.Contains(req.Input, "C-001 [done] Manual task") || strings.Contains(req.Input, "Dropped") {
				t.Errorf("bob did not see user chunk updates: %q", req.Input)
			}
			return "VOTE: AGREE", nil
		},
	})
	id, _ := eng.Start("p", "x")
	<-started
	if _, err := eng.AddChunk(id, " ", "matt"); err == nil {
		t.Fatal("blank chunk title accepted")
	}
	chunk, err := eng.AddChunk(id, "Manual task", "matt")
	if err != nil || chunk.ID != "C-001" || chunk.CreatedBy != "matt" {
		t.Fatalf("add = %+v, %v", chunk, err)
	}
	dropped, _ := eng.AddChunk(id, "Dropped", "")
	if err := eng.RemoveChunk(id, dropped.ID); err != nil {
		t.Fatal(err)
	}
	if err := eng.CompleteChunk(id, "c-001", ""); err != nil {
		t.Fatal(err)
	}
	if err := eng.CompleteChunk(id, "C-001", ""); err == nil {
		t.Fatal("completing a done chunk should fail")
	}
	close(release)
	exec := waitDone(t, eng, id)
	if exec.Status != StatusCompleted {
		t.Fatalf("status %s (%s)", exec.Status, exec.Error)
	}
	chunks, err := eng.ListChunks(id)
	if err != nil || len(chunks) != 1 || chunks[0].DoneBy != "user" {
		t.Fatalf("chunks = %+v, %v", chunks, err)
	}
	if _, err := eng.AddChunk(id, "Late", ""); err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Fatalf("add after finish err = %v", err)
	}
	if _, err := eng.AddChunk("missing", "x", ""); err == nil {
		t.Fatal("unknown execution accepted")
	}
}

func TestSessionCancelMidDiscussion(t *testing.T) {
	started := make(chan struct{}, 1)
	eng := newTestEngine(t, sessionPipeline(`
    participants: [alice, bob]
`, ""), map[string]func(context.Context, runtime.Request) (string, error){
		"alice": voter("alice", 0),
		"bob": func(ctx context.Context, _ runtime.Request) (string, error) {
			started <- struct{}{}
			<-ctx.Done()
			return "", ctx.Err()
		},
	})
	id, _ := eng.Start("p", "x")
	<-started
	if err := eng.Cancel(id); err != nil {
		t.Fatal(err)
	}
	exec := waitDone(t, eng, id)
	run := findRun(t, exec, "talk")
	if exec.Status != StatusCanceled || run.Status != StatusCanceled || len(run.Session.Turns) != 1 {
		t.Fatalf("exec %s run %s turns %d", exec.Status, run.Status, len(run.Session.Turns))
	}
}

func TestSessionRestartResumesTranscriptAndChunks(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	p, err := pipeline.Parse([]byte(sessionPipelineTwo()))
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC()
	previous := &Execution{
		ID:        "20261105-000000-bbbb",
		Pipeline:  "p",
		Input:     "topic",
		Status:    StatusRunning,
		CreatedAt: at,
		Chunks:    []Chunk{{ID: "C-003", Title: "Kept", Status: ChunkPending, CreatedAt: at}},
		NextChunk: 3,
		Runs: []*NodeRun{
			{ID: "start-run", NodeID: "start", Type: pipeline.TypeStandard, Status: StatusCompleted, Output: "start output"},
			{ID: "talk-run", NodeID: "talk", Type: pipeline.TypeSession, Status: StatusRunning, Input: "start output",
				Session: &SessionRun{MaxRounds: 1, Round: 1, Turns: []SessionTurn{{Round: 1, Participant: "alice", Output: "saved alice turn", At: at}}}},
		},
	}
	if err := store.Save(previous); err != nil {
		t.Fatal(err)
	}
	stateDir := store.stateDir
	store.Close()
	store, err = NewStore(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	reloaded, err := store.Get(previous.ID)
	if err != nil || reloaded.Status != StatusInterrupted || reloaded.Runs[1].Session == nil || len(reloaded.Runs[1].Session.Turns) != 1 {
		t.Fatalf("reloaded = %+v, %v", reloaded, err)
	}

	registry, err := runtime.NewRegistry(map[string]config.RuntimeConfig{"mock": {Type: "mock"}})
	if err != nil {
		t.Fatal(err)
	}
	registry.Register(&runtime.Mock{RuntimeName: "alice", Handler: func(context.Context, runtime.Request) (string, error) {
		t.Error("alice's completed turn was replayed")
		return "", nil
	}})
	var bobInput string
	registry.Register(&runtime.Mock{RuntimeName: "bob", Handler: func(_ context.Context, req runtime.Request) (string, error) {
		bobInput = req.Input
		return "VOTE: AGREE", nil
	}})
	eng, err := New(map[string]*pipeline.Pipeline{"p": p}, registry, store)
	if err != nil {
		t.Fatal(err)
	}
	newID, err := eng.RestartFromInterrupted(previous.ID, "talk-run")
	if err != nil {
		t.Fatal(err)
	}
	exec := waitDone(t, eng, newID)
	if exec.Status != StatusCompleted {
		t.Fatalf("status %s (%s)", exec.Status, exec.Error)
	}
	if !strings.Contains(bobInput, "saved alice turn") {
		t.Fatalf("bob input missing resumed transcript: %q", bobInput)
	}
	run := findRun(t, exec, "talk")
	if len(run.Session.Turns) != 2 || len(exec.Chunks) != 1 || exec.NextChunk != 3 {
		t.Fatalf("turns %d chunks %+v next %d", len(run.Session.Turns), exec.Chunks, exec.NextChunk)
	}
}

func findRun(t *testing.T, exec *Execution, nodeID string) *NodeRun {
	t.Helper()
	for _, run := range exec.Runs {
		if run.NodeID == nodeID {
			return run
		}
	}
	t.Fatalf("no run for %q in %+v", nodeID, exec.Runs)
	return nil
}
