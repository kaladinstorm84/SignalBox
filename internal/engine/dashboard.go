package engine

import (
	"fmt"
	"sort"

	"github.com/kaladinstorm84/SignalBox/internal/pipeline"
)

// Dashboard is a lightweight snapshot of live work and retained execution history.
type Dashboard struct {
	ActiveAgents       int            `json:"active_agents"`
	InProgress         int            `json:"in_progress"`
	WaitingApprovals   int            `json:"waiting_approvals"`
	WaitingManualEdits int            `json:"waiting_manual_edits"`
	WaitingRetries     int            `json:"waiting_retries"`
	Pipelines          int            `json:"pipelines"`
	Total              int            `json:"total"`
	Statuses           map[Status]int `json:"statuses"`
	Active             []Summary      `json:"active"`
	Recent             []Summary      `json:"recent"`
}

// Dashboard counts live nodes, including linked children, but lists only root
// executions as in-progress pipeline runs so chained work is not counted twice.
func (eng *Engine) Dashboard() (Dashboard, error) {
	eng.mu.Lock()
	defer eng.mu.Unlock()
	result := Dashboard{
		Pipelines: len(eng.pipelines),
		Statuses:  make(map[Status]int),
		Active:    make([]Summary, 0),
		Recent:    make([]Summary, 0),
	}
	var executions []*Execution
	if eng.store != nil {
		all, err := eng.store.activeExecutions()
		if err != nil {
			return Dashboard{}, err
		}
		for _, execution := range all {
			if local, ok := eng.execs[execution.ID]; ok {
				execution = local.exec
			}
			executions = append(executions, execution)
		}
	} else {
		for _, live := range eng.execs {
			executions = append(executions, live.exec)
		}
	}
	for _, execution := range executions {
		if eng.store == nil {
			result.Statuses[execution.Status]++
			result.Total++
			result.Recent = append(result.Recent, summarizeExecution(execution))
		}
		if execution.Status.Terminal() {
			continue
		}
		if execution.Parent == nil {
			result.InProgress++
			result.Active = append(result.Active, summarizeExecution(execution))
		}
		for _, run := range execution.Runs {
			switch run.Status {
			case StatusRunning:
				switch run.Type {
				case pipeline.TypeStandard, pipeline.TypeOrchestrator, pipeline.TypeSession, pipeline.TypeLLMGate:
					result.ActiveAgents++
				}
			case StatusWaitingApproval:
				result.WaitingApprovals++
			case StatusWaitingManualEdit:
				result.WaitingManualEdits++
			case StatusWaitingRetry:
				result.WaitingRetries++
			}
		}
	}
	if eng.store != nil {
		var err error
		result.Statuses, err = eng.store.statusCounts()
		if err != nil {
			return Dashboard{}, err
		}
		result.Recent, result.Total, err = eng.store.ListPage(1, 8)
		if err != nil {
			return Dashboard{}, fmt.Errorf("loading recent executions: %w", err)
		}
	}
	newestFirst := func(items []Summary) {
		sort.Slice(items, func(i, j int) bool {
			if items[i].CreatedAt.Equal(items[j].CreatedAt) {
				return items[i].ID > items[j].ID
			}
			return items[i].CreatedAt.After(items[j].CreatedAt)
		})
	}
	newestFirst(result.Active)
	newestFirst(result.Recent)
	if len(result.Active) > 8 {
		result.Active = result.Active[:8]
	}
	if len(result.Recent) > 8 {
		result.Recent = result.Recent[:8]
	}
	return result, nil
}

func (s *Store) statusCounts() (map[Status]int, error) {
	rows, err := s.db.Query("SELECT status, COUNT(*) FROM executions GROUP BY status")
	if err != nil {
		return nil, fmt.Errorf("counting execution statuses: %w", err)
	}
	defer rows.Close()
	counts := make(map[Status]int)
	for rows.Next() {
		var status Status
		var count int
		if err := rows.Scan(&status, &count); err != nil {
			return nil, fmt.Errorf("reading execution status count: %w", err)
		}
		counts[status] = count
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reading execution status counts: %w", err)
	}
	return counts, nil
}
