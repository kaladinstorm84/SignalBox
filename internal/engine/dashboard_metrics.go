package engine

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"time"
)

// DashboardOptions selects the creation-time cohort used by analytics.
type DashboardOptions struct {
	Range    string
	Pipeline string
}

func (o DashboardOptions) since(now time.Time) (time.Time, error) {
	switch o.Range {
	case "", "7d":
		return now.Add(-7 * 24 * time.Hour), nil
	case "24h":
		return now.Add(-24 * time.Hour), nil
	case "30d":
		return now.Add(-30 * 24 * time.Hour), nil
	case "all":
		return time.Time{}, nil
	default:
		return time.Time{}, fmt.Errorf("range must be 24h, 7d, 30d, or all")
	}
}

type DurationMetrics struct {
	Samples int      `json:"samples"`
	Average *float64 `json:"average_ms"`
	Median  *float64 `json:"median_ms"`
	P95     *float64 `json:"p95_ms"`
}

type OutcomeMetrics struct {
	Total            int             `json:"total"`
	Terminal         int             `json:"terminal"`
	Statuses         map[Status]int  `json:"statuses"`
	SuccessRate      *float64        `json:"success_rate"`
	FailureRate      *float64        `json:"failure_rate"`
	Retried          int             `json:"retried"`
	RetryRate        *float64        `json:"retry_rate"`
	Recovered        int             `json:"recovered"`
	CallAttempts     int             `json:"call_attempts"`
	FailedCalls      int             `json:"failed_calls"`
	CallFailureRate  *float64        `json:"call_failure_rate"`
	CompletedPerHour *float64        `json:"completed_per_hour"`
	Duration         DurationMetrics `json:"duration"`
}

type PipelineMetrics struct {
	Pipeline string `json:"pipeline"`
	OutcomeMetrics
	LastFailure *time.Time `json:"last_failure"`
}

type DailyMetrics struct {
	Date string `json:"date"`
	OutcomeMetrics
}

type NodeMetrics struct {
	Pipeline string          `json:"pipeline"`
	Node     string          `json:"node"`
	Duration DurationMetrics `json:"duration"`
}

type ActiveDuration struct {
	ID        string  `json:"id"`
	Pipeline  string  `json:"pipeline"`
	ElapsedMS float64 `json:"elapsed_ms"`
}

type DashboardAnalytics struct {
	Range         string            `json:"range"`
	Pipeline      string            `json:"pipeline"`
	Since         *time.Time        `json:"since"`
	AsOf          time.Time         `json:"as_of"`
	PipelineNames []string          `json:"pipeline_names"`
	Roots         OutcomeMetrics    `json:"roots"`
	Children      OutcomeMetrics    `json:"children"`
	Pipelines     []PipelineMetrics `json:"pipelines"`
	Daily         []DailyMetrics    `json:"daily"`
	SlowNodes     []NodeMetrics     `json:"slow_nodes"`
	LongestActive []ActiveDuration  `json:"longest_active"`
}

type metricRun struct {
	NodeID      string     `json:"node_id"`
	ReusedFrom  string     `json:"reused_from"`
	Retries     int        `json:"retries"`
	Attempts    int        `json:"attempts"`
	FailedCalls int        `json:"failed_calls"`
	StartedAt   *time.Time `json:"started_at"`
	FinishedAt  *time.Time `json:"finished_at"`
}

type metricExecution struct {
	ID, Pipeline, Parent string
	Status               Status
	CreatedAt            time.Time
	FinishedAt           *time.Time
	Runs                 []metricRun
}

// Project only reporting fields; prompts, outputs, transcripts and snapshots
// must not be decoded on each dashboard poll.
func (s *Store) dashboardExecutions(since time.Time, pipeline string) ([]metricExecution, error) {
	query := `SELECT id, pipeline, status, created_at,
		COALESCE(json_extract(payload, '$.parent.execution'), ''),
		json_extract(payload, '$.finished_at'),
		(SELECT json_group_array(json_object(
			'node_id', json_extract(r.value, '$.node_id'),
			'reused_from', json_extract(r.value, '$.reused_from'),
			'retries', COALESCE(json_extract(r.value, '$.retries'), 0),
			'started_at', json_extract(r.value, '$.started_at'),
			'finished_at', json_extract(r.value, '$.finished_at'),
			'attempts', (SELECT COUNT(*) FROM json_each(r.value, '$.call_attempts')),
			'failed_calls', (SELECT COUNT(*) FROM json_each(r.value, '$.call_attempts') a
				WHERE COALESCE(json_extract(a.value, '$.error'), '') != '')
		)) FROM json_each(payload, '$.runs') r)
		FROM executions WHERE 1=1`
	var args []any
	if !since.IsZero() {
		query += " AND created_at >= ?"
		args = append(args, since.UnixNano())
	}
	if pipeline != "" {
		query += " AND pipeline = ?"
		args = append(args, pipeline)
	}
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("loading dashboard analytics: %w", err)
	}
	defer rows.Close()
	var result []metricExecution
	for rows.Next() {
		var e metricExecution
		var created int64
		var finished *string
		var runs string
		if err := rows.Scan(&e.ID, &e.Pipeline, &e.Status, &created, &e.Parent, &finished, &runs); err != nil {
			return nil, fmt.Errorf("reading dashboard analytics: %w", err)
		}
		e.CreatedAt = time.Unix(0, created).UTC()
		if finished != nil {
			t, err := time.Parse(time.RFC3339Nano, *finished)
			if err != nil {
				return nil, fmt.Errorf("reading dashboard finish time for %q: %w", e.ID, err)
			}
			e.FinishedAt = &t
		}
		if err := json.Unmarshal([]byte(runs), &e.Runs); err != nil {
			return nil, fmt.Errorf("reading dashboard node metrics for %q: %w", e.ID, err)
		}
		result = append(result, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reading dashboard analytics: %w", err)
	}
	return result, nil
}

func executionMetrics(e *Execution) metricExecution {
	m := metricExecution{ID: e.ID, Pipeline: e.Pipeline, Status: e.Status, CreatedAt: e.CreatedAt, FinishedAt: e.FinishedAt}
	if e.Parent != nil {
		m.Parent = e.Parent.Execution
	}
	for _, r := range e.Runs {
		n := metricRun{NodeID: r.NodeID, ReusedFrom: r.ReusedFrom, Retries: r.Retries,
			StartedAt: r.StartedAt, FinishedAt: r.FinishedAt, Attempts: len(r.CallAttempts)}
		for _, a := range r.CallAttempts {
			if a.Error != "" {
				n.FailedCalls++
			}
		}
		m.Runs = append(m.Runs, n)
	}
	return m
}

type outcomeAccumulator struct {
	OutcomeMetrics
	durations []float64
}

func (a *outcomeAccumulator) add(e metricExecution) {
	if a.Statuses == nil {
		a.Statuses = make(map[Status]int)
	}
	a.Total++
	a.Statuses[e.Status]++
	if e.Status.Terminal() {
		a.Terminal++
		if e.FinishedAt != nil && !e.FinishedAt.Before(e.CreatedAt) {
			a.durations = append(a.durations, float64(e.FinishedAt.Sub(e.CreatedAt))/float64(time.Millisecond))
		}
	}
	retried := false
	for _, r := range e.Runs {
		if r.ReusedFrom != "" {
			continue
		}
		retried = retried || r.Retries > 0
		a.CallAttempts += r.Attempts
		a.FailedCalls += r.FailedCalls
	}
	if retried {
		a.Retried++
		if e.Status == StatusCompleted {
			a.Recovered++
		}
	}
}

func percentage(n, total int) *float64 {
	if total == 0 {
		return nil
	}
	v := 100 * float64(n) / float64(total)
	return &v
}

func durations(values []float64) DurationMetrics {
	m := DurationMetrics{Samples: len(values)}
	if len(values) == 0 {
		return m
	}
	sort.Float64s(values)
	var sum float64
	for _, v := range values {
		sum += v
	}
	avg := sum / float64(len(values))
	median := values[len(values)/2]
	if len(values)%2 == 0 {
		median = (values[len(values)/2-1] + median) / 2
	}
	p95 := values[int(math.Ceil(.95*float64(len(values))))-1]
	m.Average, m.Median, m.P95 = &avg, &median, &p95
	return m
}

func (a *outcomeAccumulator) finish(hours float64) OutcomeMetrics {
	if a.Statuses == nil {
		a.Statuses = make(map[Status]int)
	}
	a.SuccessRate = percentage(a.Statuses[StatusCompleted], a.Terminal)
	a.FailureRate = percentage(a.Statuses[StatusFailed], a.Terminal)
	a.RetryRate = percentage(a.Retried, a.Total)
	a.CallFailureRate = percentage(a.FailedCalls, a.CallAttempts)
	a.Duration = durations(a.durations)
	if hours > 0 {
		rate := float64(a.Statuses[StatusCompleted]) / hours
		a.CompletedPerHour = &rate
	}
	return a.OutcomeMetrics
}

func (eng *Engine) dashboardAnalytics(o DashboardOptions, now time.Time) (DashboardAnalytics, error) {
	since, err := o.since(now)
	if err != nil {
		return DashboardAnalytics{}, err
	}
	if o.Range == "" {
		o.Range = "7d"
	}
	result := DashboardAnalytics{
		Range: o.Range, Pipeline: o.Pipeline, AsOf: now,
		PipelineNames: []string{}, Pipelines: []PipelineMetrics{}, Daily: []DailyMetrics{},
		SlowNodes: []NodeMetrics{}, LongestActive: []ActiveDuration{},
	}
	if !since.IsZero() {
		result.Since = &since
	}
	names := make(map[string]bool)
	for name := range eng.pipelines {
		names[name] = true
	}
	var records []metricExecution
	if eng.store != nil {
		// Pipeline options include retained definitions that are no longer loaded.
		rows, err := eng.store.db.Query("SELECT DISTINCT pipeline FROM executions")
		if err != nil {
			return result, fmt.Errorf("loading dashboard pipeline names: %w", err)
		}
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				rows.Close()
				return result, fmt.Errorf("reading dashboard pipeline names: %w", err)
			}
			names[name] = true
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return result, fmt.Errorf("reading dashboard pipeline names: %w", err)
		}
		records, err = eng.store.dashboardExecutions(since, o.Pipeline)
		if err != nil {
			return result, err
		}
		for i, e := range records {
			if live, ok := eng.execs[e.ID]; ok {
				records[i] = executionMetrics(live.exec)
			}
		}
	} else {
		for _, live := range eng.execs {
			names[live.exec.Pipeline] = true
			records = append(records, executionMetrics(live.exec))
		}
	}
	for name := range names {
		result.PipelineNames = append(result.PipelineNames, name)
	}
	sort.Strings(result.PipelineNames)
	var roots, children outcomeAccumulator
	pipelines := make(map[string]*outcomeAccumulator)
	failures := make(map[string]time.Time)
	days := make(map[string]*outcomeAccumulator)
	nodes := make(map[[2]string][]float64)
	start := since
	trendStart := now.UTC().Truncate(24*time.Hour).AddDate(0, 0, -29)
	if !since.IsZero() && since.After(trendStart) {
		trendStart = since.UTC().Truncate(24 * time.Hour)
	}
	for d := trendStart; !d.After(now); d = d.AddDate(0, 0, 1) {
		days[d.Format("2006-01-02")] = &outcomeAccumulator{}
	}
	for _, e := range records {
		if e.CreatedAt.Before(since) || e.CreatedAt.After(now) || (o.Pipeline != "" && e.Pipeline != o.Pipeline) {
			continue
		}
		if start.IsZero() || e.CreatedAt.Before(start) {
			start = e.CreatedAt
		}
		if e.Parent != "" {
			children.add(e)
		} else {
			roots.add(e)
			if pipelines[e.Pipeline] == nil {
				pipelines[e.Pipeline] = &outcomeAccumulator{}
			}
			pipelines[e.Pipeline].add(e)
			if e.Status == StatusFailed {
				at := e.CreatedAt
				if e.FinishedAt != nil {
					at = *e.FinishedAt
				}
				if at.After(failures[e.Pipeline]) {
					failures[e.Pipeline] = at
				}
			}
			if day := days[e.CreatedAt.UTC().Format("2006-01-02")]; day != nil {
				day.add(e)
			}
			if !e.Status.Terminal() {
				result.LongestActive = append(result.LongestActive, ActiveDuration{
					ID: e.ID, Pipeline: e.Pipeline, ElapsedMS: float64(now.Sub(e.CreatedAt)) / float64(time.Millisecond),
				})
			}
		}
		for _, r := range e.Runs {
			if r.ReusedFrom == "" && r.StartedAt != nil && r.FinishedAt != nil && !r.FinishedAt.Before(*r.StartedAt) {
				key := [2]string{e.Pipeline, r.NodeID}
				nodes[key] = append(nodes[key], float64(r.FinishedAt.Sub(*r.StartedAt))/float64(time.Millisecond))
			}
		}
	}
	hours := now.Sub(start).Hours()
	if start.IsZero() {
		hours = 0
	}
	result.Roots, result.Children = roots.finish(hours), children.finish(hours)
	for name, a := range pipelines {
		p := PipelineMetrics{Pipeline: name, OutcomeMetrics: a.finish(hours)}
		if at, ok := failures[name]; ok {
			p.LastFailure = &at
		}
		result.Pipelines = append(result.Pipelines, p)
	}
	sort.Slice(result.Pipelines, func(i, j int) bool { return result.Pipelines[i].Pipeline < result.Pipelines[j].Pipeline })
	for date, a := range days {
		result.Daily = append(result.Daily, DailyMetrics{Date: date, OutcomeMetrics: a.finish(0)})
	}
	sort.Slice(result.Daily, func(i, j int) bool { return result.Daily[i].Date < result.Daily[j].Date })
	for key, values := range nodes {
		result.SlowNodes = append(result.SlowNodes, NodeMetrics{Pipeline: key[0], Node: key[1], Duration: durations(values)})
	}
	sort.Slice(result.SlowNodes, func(i, j int) bool {
		a, b := result.SlowNodes[i], result.SlowNodes[j]
		if *a.Duration.Average != *b.Duration.Average {
			return *a.Duration.Average > *b.Duration.Average
		}
		if a.Pipeline != b.Pipeline {
			return a.Pipeline < b.Pipeline
		}
		return a.Node < b.Node
	})
	if len(result.SlowNodes) > 8 {
		result.SlowNodes = result.SlowNodes[:8]
	}
	sort.Slice(result.LongestActive, func(i, j int) bool {
		a, b := result.LongestActive[i], result.LongestActive[j]
		if a.ElapsedMS == b.ElapsedMS {
			return a.ID < b.ID
		}
		return a.ElapsedMS > b.ElapsedMS
	})
	if len(result.LongestActive) > 8 {
		result.LongestActive = result.LongestActive[:8]
	}
	return result, nil
}
