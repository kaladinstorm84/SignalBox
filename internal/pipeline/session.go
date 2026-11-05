package pipeline

import (
	"fmt"
	"strings"
)

// validateSession checks a session node's own configuration and fills in
// defaults so the engine can rely on a complete configuration.
func (p *Pipeline) validateSession(n *Node) error {
	if n.Runtime != "" || n.Prompt != "" || len(n.Stages) > 0 {
		return fmt.Errorf("session %q may not define runtime, prompt, or stages; participants supply them", n.ID)
	}
	if len(n.Participants) < 2 {
		return fmt.Errorf("session %q needs at least two participants", n.ID)
	}
	seen := map[string]bool{}
	for _, id := range n.Participants {
		participant, ok := p.byID[id]
		if !ok {
			return fmt.Errorf("session %q lists unknown participant %q", n.ID, id)
		}
		if id == n.ID {
			return fmt.Errorf("session %q may not list itself as a participant", n.ID)
		}
		if participant.Type != TypeStandard {
			return fmt.Errorf("session %q: participant %q must be a standard agent", n.ID, id)
		}
		if seen[id] {
			return fmt.Errorf("session %q lists participant %q more than once", n.ID, id)
		}
		seen[id] = true
	}

	switch n.TurnOrder {
	case "":
		n.TurnOrder = TurnOrderRoundRobin
	case TurnOrderRoundRobin, TurnOrderModerator:
	default:
		return fmt.Errorf("session %q: turn_order must be round_robin or moderator", n.ID)
	}
	if n.TurnOrder == TurnOrderModerator {
		if n.Moderator == "" {
			return fmt.Errorf("session %q: turn_order moderator requires a moderator", n.ID)
		}
		if !seen[n.Moderator] {
			return fmt.Errorf("session %q: moderator %q must be a participant", n.ID, n.Moderator)
		}
	} else if n.Moderator != "" {
		return fmt.Errorf("session %q: moderator is only used with turn_order moderator", n.ID)
	}

	if n.MaxRounds == 0 {
		n.MaxRounds = DefaultMaxRounds
	}
	if n.MaxRounds < 1 || n.MaxRounds > MaxSessionRounds {
		return fmt.Errorf("session %q: max_rounds must be between 1 and %d", n.ID, MaxSessionRounds)
	}
	if n.SummaryAgent != "" && !seen[n.SummaryAgent] {
		return fmt.Errorf("session %q: summary_agent %q must be a participant", n.ID, n.SummaryAgent)
	}

	if n.Consensus == nil {
		n.Consensus = &Consensus{}
	}
	c := n.Consensus
	switch c.Method {
	case "", ConsensusVote:
		c.Method = ConsensusVote
		if c.Keyword == "" {
			c.Keyword = DefaultConsensusKeyword
		}
		if strings.TrimSpace(c.Keyword) == "" || strings.ContainsAny(c.Keyword, "\r\n") {
			return fmt.Errorf("session %q: consensus.keyword must be a non-blank single line", n.ID)
		}
		c.Keyword = strings.TrimSpace(c.Keyword)
		switch c.Require {
		case "":
			c.Require = ConsensusRequireAll
		case ConsensusRequireAll, ConsensusRequireMajority:
		default:
			return fmt.Errorf("session %q: consensus.require must be all or majority", n.ID)
		}
	case ConsensusModeratorDecide:
		if c.Keyword != "" || c.Require != "" {
			return fmt.Errorf("session %q: consensus.keyword and consensus.require only apply to the vote method", n.ID)
		}
		if n.SummaryAgent == "" {
			return fmt.Errorf("session %q: consensus method moderator_decides requires a summary_agent", n.ID)
		}
	default:
		return fmt.Errorf("session %q: consensus.method must be vote or moderator_decides", n.ID)
	}

	if n.Chunks != nil && !n.Chunks.Enabled && len(n.Chunks.Initial) > 0 {
		return fmt.Errorf("session %q: chunks.initial requires chunks.enabled", n.ID)
	}
	if c.RequireChunksDone && !n.ChunksEnabled() {
		return fmt.Errorf("session %q: consensus.require_chunks_done requires chunks.enabled", n.ID)
	}
	if n.Chunks != nil {
		if len(n.Chunks.Initial) > MaxInitialChunks {
			return fmt.Errorf("session %q: chunks.initial may list at most %d chunks", n.ID, MaxInitialChunks)
		}
		for i, title := range n.Chunks.Initial {
			if err := ValidateChunkTitle(title); err != nil {
				return fmt.Errorf("session %q: chunks.initial[%d]: %w", n.ID, i, err)
			}
			n.Chunks.Initial[i] = strings.TrimSpace(title)
		}
	}
	return nil
}

// ValidateChunkTitle checks a chunk title from YAML, a directive, or the API.
func ValidateChunkTitle(title string) error {
	title = strings.TrimSpace(title)
	if title == "" {
		return fmt.Errorf("chunk title must not be blank")
	}
	if strings.ContainsAny(title, "\r\n") {
		return fmt.Errorf("chunk title must be a single line")
	}
	if len([]rune(title)) > MaxChunkTitleLength {
		return fmt.Errorf("chunk title must be at most %d characters", MaxChunkTitleLength)
	}
	return nil
}

// checkSessionParticipants ensures participants are only ever invoked by
// their session, so they never run as independent pipeline steps.
func (p *Pipeline) checkSessionParticipants() error {
	owner := map[string]string{}
	for i := range p.Nodes {
		session := &p.Nodes[i]
		for _, id := range session.Participants {
			if other, ok := owner[id]; ok {
				return fmt.Errorf("agent %q is a participant of both session %q and session %q", id, other, session.ID)
			}
			owner[id] = session.ID
		}
	}
	for id, sessionID := range owner {
		participant := p.byID[id]
		if id == p.Entry {
			return fmt.Errorf("session %q: participant %q may not be the pipeline entry", sessionID, id)
		}
		if len(participant.Handoff) > 0 || participant.RejectTo != "" || len(participant.AdditionalInput) > 0 || participant.Retry != nil {
			return fmt.Errorf("session %q: participant %q may not define handoff, reject-to, additional-input, or retry", sessionID, id)
		}
	}
	for i := range p.Nodes {
		n := &p.Nodes[i]
		for _, target := range n.HandoffTargets() {
			if sessionID, ok := owner[target]; ok {
				return fmt.Errorf("agent %q hands off to %q, which is a participant of session %q", n.ID, target, sessionID)
			}
		}
		if sessionID, ok := owner[n.RejectTo]; ok {
			return fmt.Errorf("gate %q rejects to %q, which is a participant of session %q", n.ID, n.RejectTo, sessionID)
		}
		for _, inputID := range n.AdditionalInput {
			if sessionID, ok := owner[inputID]; ok {
				return fmt.Errorf("agent %q requires additional input from %q, which is a participant of session %q; use the session output instead", n.ID, inputID, sessionID)
			}
		}
		for _, s := range n.Stages {
			for _, agent := range s.Agents {
				if sessionID, ok := owner[agent]; ok {
					return fmt.Errorf("orchestrator %q: stage %q dispatches to %q, which is a participant of session %q", n.ID, s.ID, agent, sessionID)
				}
			}
		}
	}
	return nil
}
