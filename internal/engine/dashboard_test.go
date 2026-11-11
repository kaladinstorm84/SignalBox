package engine

import (
	"fmt"
	"testing"
	"time"

	"github.com/kaladinstorm84/SignalBox/internal/pipeline"
)

func TestDashboard(t *testing.T) {
	for _, persistent := range []bool{false, true} {
		t.Run(fmt.Sprintf("persistent=%t", persistent), func(t *testing.T) {
			eng := &Engine{
				pipelines: map[string]*pipeline.Pipeline{"demo": {}, "child": {}},
				execs:     make(map[string]*liveExec),
			}
			if persistent {
				store, err := NewStore(t.TempDir(), 0)
				if err != nil {
					t.Fatal(err)
				}
				defer store.Close()
				eng.store = store
			}
			add := func(execution *Execution, live bool) {
				t.Helper()
				if eng.store != nil {
					if err := eng.store.Save(execution); err != nil {
						t.Fatal(err)
					}
				}
				if live || eng.store == nil {
					eng.execs[execution.ID] = &liveExec{exec: execution}
				}
			}
			for i := 0; i < 125; i++ {
				status := StatusCompleted
				if i < 5 {
					status = StatusFailed
				}
				add(&Execution{
					ID: fmt.Sprintf("history-%03d", i), Pipeline: "demo", Status: status,
					CreatedAt: time.Unix(int64(i), 0),
					Runs:      []*NodeRun{{Status: StatusRunning, Type: pipeline.TypeStandard}},
				}, false)
			}
			root := &Execution{
				ID: "root", Pipeline: "demo", Status: StatusRunning, CreatedAt: time.Unix(200, 0),
				Runs: []*NodeRun{
					{Status: StatusRunning, Type: pipeline.TypeStandard},
					{Status: StatusRunning, Type: pipeline.TypeOrchestrator},
					{Status: StatusRunning, Type: pipeline.TypeSession},
					{Status: StatusRunning, Type: pipeline.TypeLLMGate},
					{Status: StatusRunning, Type: pipeline.TypeCode},
					{Status: StatusRunning, Type: pipeline.TypePipeline},
					{Status: StatusPending, Type: pipeline.TypeStandard},
					{Status: StatusCompleted, Type: pipeline.TypeStandard},
					{Status: StatusWaitingApproval, Type: pipeline.TypeApproval},
				},
			}
			add(root, true)
			child := &Execution{
				ID: "child", Pipeline: "child", Status: StatusRunning, CreatedAt: time.Unix(201, 0),
				Parent: &ParentLink{Execution: "root"},
				Runs: []*NodeRun{
					{Status: StatusRunning, Type: pipeline.TypeStandard},
					{Status: StatusWaitingManualEdit, Type: pipeline.TypeManualEdit},
					{Status: StatusWaitingRetry, Type: pipeline.TypeStandard},
				},
			}
			add(child, true)
			for i := 0; i < 10; i++ {
				add(&Execution{
					ID: fmt.Sprintf("waiting-%02d", i), Pipeline: "demo", Status: StatusPending,
					CreatedAt: time.Unix(int64(300+i), 0),
				}, true)
			}
			dashboard, err := eng.Dashboard()
			if err != nil {
				t.Fatal(err)
			}
			if dashboard.ActiveAgents != 5 || dashboard.InProgress != 11 || dashboard.Pipelines != 2 {
				t.Fatalf("live counts = %+v", dashboard)
			}
			if dashboard.WaitingApprovals != 1 || dashboard.WaitingManualEdits != 1 || dashboard.WaitingRetries != 1 {
				t.Fatalf("attention counts = %+v", dashboard)
			}
			if dashboard.Total != 137 || dashboard.Statuses[StatusCompleted] != 120 || dashboard.Statuses[StatusFailed] != 5 {
				t.Fatalf("history counts = %+v", dashboard)
			}
			if len(dashboard.Active) != 8 || len(dashboard.Recent) != 8 || dashboard.Active[0].ID != "waiting-09" || dashboard.Recent[7].ID != "waiting-02" {
				t.Fatalf("bounded newest-first lists = %+v", dashboard)
			}
			active, err := eng.ActiveDashboard()
			if err != nil {
				t.Fatal(err)
			}
			if active.InProgress != dashboard.InProgress || len(active.Active) != 8 || active.Active[0].ID != dashboard.Active[0].ID {
				t.Fatalf("active dashboard = %+v", active)
			}
			root.Status, child.Status = StatusCompleted, StatusCanceled
			dashboard, err = eng.Dashboard()
			if err != nil {
				t.Fatal(err)
			}
			if dashboard.ActiveAgents != 0 || dashboard.InProgress != 10 ||
				dashboard.WaitingApprovals+dashboard.WaitingManualEdits+dashboard.WaitingRetries != 0 {
				t.Fatalf("terminal executions counted as active: %+v", dashboard)
			}
			active, err = eng.ActiveDashboard()
			if err != nil || active.InProgress != 10 {
				t.Fatalf("terminal executions in active dashboard: %+v, %v", active, err)
			}
		})
	}
}

func TestDashboardStoreError(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := (&Engine{store: store}).Dashboard(); err == nil {
		t.Fatal("expected dashboard to report the store failure")
	}
	if _, err := (&Engine{store: store}).ActiveDashboard(); err == nil {
		t.Fatal("expected active dashboard to report the store failure")
	}
}
