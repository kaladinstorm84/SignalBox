package engine

import (
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"testing"
	"time"

	"github.com/kaladinstorm84/SignalBox/internal/pipeline"
)

func TestDashboardAnalytics(t *testing.T) {
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	timestamp := func(t time.Time) *time.Time { return &t }
	for _, persistent := range []bool{false, true} {
		t.Run(fmt.Sprintf("persistent=%t", persistent), func(t *testing.T) {
			eng := &Engine{pipelines: map[string]*pipeline.Pipeline{"loaded": {}}, execs: make(map[string]*liveExec)}
			if persistent {
				store, err := NewStore(t.TempDir(), 0)
				if err != nil {
					t.Fatal(err)
				}
				defer store.Close()
				eng.store = store
			}
			add := func(e *Execution) {
				t.Helper()
				if persistent {
					if err := eng.store.Save(e); err != nil {
						t.Fatal(err)
					}
				} else {
					eng.execs[e.ID] = &liveExec{exec: e}
				}
			}
			for i, status := range []Status{StatusCompleted, StatusCompleted, StatusFailed, StatusCanceled, StatusRejected, StatusInterrupted} {
				created := now.Add(-time.Hour)
				e := &Execution{ID: fmt.Sprint(i), Pipeline: "history", Status: status, CreatedAt: created,
					FinishedAt: timestamp(created.Add(time.Duration(i+1) * time.Second)),
					Runs:       []*NodeRun{{NodeID: "work", StartedAt: &created, FinishedAt: timestamp(created.Add(500 * time.Millisecond))}}}
				if i == 0 {
					e.Runs[0].Retries = 1
					e.Runs[0].CallAttempts = []CallAttempt{{Error: "failed"}, {}}
					e.Runs = append(e.Runs, &NodeRun{NodeID: "reused", ReusedFrom: "old", Retries: 5,
						StartedAt: &created, FinishedAt: timestamp(now), CallAttempts: []CallAttempt{{Error: "old"}}})
				}
				if i == 2 {
					// Invalid timing still contributes to outcomes, not durations.
					e.FinishedAt = timestamp(created.Add(-time.Second))
				}
				if i == 3 {
					e.FinishedAt = nil
				}
				add(e)
			}
			add(&Execution{ID: "child", Pipeline: "child-pipeline", Parent: &ParentLink{Execution: "0"},
				Status: StatusFailed, CreatedAt: now.Add(-2 * time.Hour)})
			add(&Execution{ID: "active", Pipeline: "history", Status: StatusRunning, CreatedAt: now.Add(-2 * time.Hour)})
			add(&Execution{ID: "old", Pipeline: "history", Status: StatusFailed, CreatedAt: now.Add(-8 * 24 * time.Hour)})
			add(&Execution{ID: "future", Pipeline: "history", Status: StatusCompleted, CreatedAt: now.Add(time.Hour)})
			a, err := eng.dashboardAnalytics(DashboardOptions{}, now)
			if err != nil {
				t.Fatal(err)
			}
			m := a.Roots
			if m.Total != 7 || m.Terminal != 6 || m.Statuses[StatusCompleted] != 2 || a.Children.Total != 1 {
				t.Fatalf("outcomes = %+v, children = %+v", m, a.Children)
			}
			assertNear := func(name string, got *float64, want float64) {
				t.Helper()
				if got == nil || math.Abs(*got-want) > .00001 {
					t.Fatalf("%s = %v, want %v", name, got, want)
				}
			}
			assertNear("success", m.SuccessRate, 100.0/3)
			assertNear("failure", m.FailureRate, 100.0/6)
			assertNear("retry", m.RetryRate, 100.0/7)
			assertNear("call failure", m.CallFailureRate, 50)
			assertNear("throughput", m.CompletedPerHour, 2.0/(7*24))
			assertNear("median", m.Duration.Median, 3500)
			assertNear("average", m.Duration.Average, 3500)
			assertNear("p95", m.Duration.P95, 6000)
			if m.Duration.Samples != 4 || m.Retried != 1 || m.Recovered != 1 || m.CallAttempts != 2 || m.FailedCalls != 1 {
				t.Fatalf("reliability = %+v", m)
			}
			if len(a.SlowNodes) != 1 || a.SlowNodes[0].Node != "work" || a.SlowNodes[0].Duration.Samples != 6 {
				t.Fatalf("slow nodes = %+v", a.SlowNodes)
			}
			if len(a.LongestActive) != 1 || a.LongestActive[0].ID != "active" || a.LongestActive[0].ElapsedMS != 7200000 {
				t.Fatalf("active = %+v", a.LongestActive)
			}
			if !reflect.DeepEqual(a.PipelineNames, []string{"child-pipeline", "history", "loaded"}) {
				t.Fatalf("pipeline options = %+v", a.PipelineNames)
			}
			if len(a.Daily) != 8 || a.Daily[len(a.Daily)-1].Total != 7 || a.Daily[0].Total != 0 {
				t.Fatalf("daily metrics = %+v", a.Daily)
			}
			if len(a.Pipelines) != 1 || a.Pipelines[0].LastFailure == nil {
				t.Fatalf("pipeline metrics = %+v", a.Pipelines)
			}
			filtered, err := eng.dashboardAnalytics(DashboardOptions{Range: "all", Pipeline: "child-pipeline"}, now)
			if err != nil {
				t.Fatal(err)
			}
			if filtered.Roots.Total != 0 || filtered.Children.Total != 1 || len(filtered.Daily) != 30 || filtered.Roots.SuccessRate != nil {
				t.Fatalf("filtered metrics = %+v", filtered)
			}
			all, err := eng.dashboardAnalytics(DashboardOptions{Range: "all"}, now)
			if err != nil {
				t.Fatal(err)
			}
			if all.Roots.Total != 8 || all.Since != nil {
				t.Fatalf("all history = %+v", all)
			}
			// A local live record supersedes the last persisted snapshot.
			eng.execs["active"] = &liveExec{exec: &Execution{ID: "active", Pipeline: "history",
				Status: StatusCompleted, CreatedAt: now.Add(-2 * time.Hour), FinishedAt: timestamp(now)}}
			updated, err := eng.dashboardAnalytics(DashboardOptions{Range: "24h", Pipeline: "history"}, now)
			if err != nil {
				t.Fatal(err)
			}
			if updated.Roots.Statuses[StatusCompleted] != 3 || len(updated.LongestActive) != 0 {
				t.Fatalf("local overlay = %+v", updated)
			}
		})
	}
}

func TestDashboardAnalyticsEmptyAndValidation(t *testing.T) {
	eng := &Engine{}
	now := time.Now().UTC()
	for _, window := range []string{"24h", "7d", "30d", "all"} {
		a, err := eng.dashboardAnalytics(DashboardOptions{Range: window}, now)
		if err != nil {
			t.Fatal(err)
		}
		if a.Roots.SuccessRate != nil || a.Roots.FailureRate != nil || a.Roots.RetryRate != nil || a.Roots.Duration.Median != nil {
			t.Fatalf("empty rates = %+v", a.Roots)
		}
		if _, err := json.Marshal(a); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := eng.DashboardWithOptions(DashboardOptions{Range: "invalid"}); err == nil {
		t.Fatal("invalid range accepted")
	}
}

func TestDashboardAnalyticsFullHistoryAndBounds(t *testing.T) {
	now := time.Now().UTC()
	eng := &Engine{execs: make(map[string]*liveExec)}
	for i := 0; i < 125; i++ {
		start := now.Add(-time.Hour)
		finish := start.Add(time.Duration(i+1) * time.Second)
		id := fmt.Sprint(i)
		eng.execs[id] = &liveExec{exec: &Execution{ID: id, Pipeline: "p", Status: StatusCompleted,
			CreatedAt: start, FinishedAt: &finish,
			Runs: []*NodeRun{{NodeID: id, StartedAt: &start, FinishedAt: &finish}}}}
	}

	a, err := eng.Dashboard()
	if err != nil {
		t.Fatal(err)
	}
	if a.Analytics.Roots.Total != 125 || a.Analytics.Roots.Duration.Samples != 125 || len(a.Recent) != 8 || len(a.Analytics.SlowNodes) != 8 {
		t.Fatalf("history coverage = %+v", a.Analytics)
	}
	if *a.Analytics.Roots.Duration.P95 != 119000 {
		t.Fatalf("p95 = %v", *a.Analytics.Roots.Duration.P95)
	}
}

func TestDashboardAnalyticsStoreErrors(t *testing.T) {
	store, err := NewStore(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	eng := &Engine{store: store}
	now := time.Now().UTC()
	e := &Execution{ID: "corrupt", Pipeline: "p", Status: StatusCompleted, CreatedAt: now.Add(-time.Hour)}
	if err := store.Save(e); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE executions SET payload = ?`, `{"finished_at":"invalid"}`); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.dashboardAnalytics(DashboardOptions{}, now); err == nil {
		t.Fatal("invalid metric timestamp was silently accepted")
	}
	active, err := eng.ActiveDashboard()
	if err != nil || active.InProgress != 0 {
		t.Fatalf("active polling must not read terminal analytics: %+v, %v", active, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.dashboardAnalytics(DashboardOptions{}, now); err == nil {
		t.Fatal("store failure was silently accepted")
	}
}
