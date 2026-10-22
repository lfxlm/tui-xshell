package scheduledtransfer

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"tui-ssh-workbench/internal/model"
)

type runnerTestRepository struct {
	mu        sync.Mutex
	transfer  model.ScheduledTransfer
	claimed   bool
	claimN    int
	renewN    int
	finishN   int
	finishErr error
}

func (repo *runnerTestRepository) ListDueScheduledTransfers(_ context.Context, now time.Time, limit int) ([]model.ScheduledTransfer, error) {
	repo.mu.Lock()
	defer repo.mu.Unlock()
	if limit < 1 || repo.claimed || (!repo.transfer.RunRequested && (!repo.transfer.Enabled || repo.transfer.NextRunAt.After(now))) {
		return nil, nil
	}
	return []model.ScheduledTransfer{repo.transfer}, nil
}

func (repo *runnerTestRepository) ClaimScheduledTransfer(_ context.Context, id, token string, runRequested bool, now, nextRunAt, leaseUntil time.Time) (bool, error) {
	repo.mu.Lock()
	defer repo.mu.Unlock()
	if repo.claimed || id != repo.transfer.Id || runRequested != repo.transfer.RunRequested || (!runRequested && repo.transfer.NextRunAt.After(now)) {
		return false, nil
	}
	repo.claimed = true
	repo.claimN++
	if !runRequested {
		repo.transfer.NextRunAt = nextRunAt
	}
	repo.transfer.RunRequested = false
	repo.transfer.LeaseUntil = leaseUntil
	repo.transfer.LeaseToken = token
	repo.transfer.LastStatus = model.ScheduledTransferRunning
	return true, nil
}

func (repo *runnerTestRepository) RenewScheduledTransferLease(_ context.Context, id, token string, leaseUntil time.Time) error {
	repo.mu.Lock()
	defer repo.mu.Unlock()
	if id != repo.transfer.Id || repo.transfer.LastStatus != model.ScheduledTransferRunning || token != repo.transfer.LeaseToken {
		return errors.New("not running")
	}
	repo.renewN++
	repo.transfer.LeaseUntil = leaseUntil
	return nil
}

func (repo *runnerTestRepository) FinishScheduledTransfer(_ context.Context, id, token string, status model.ScheduledTransferStatus, finishedAt time.Time, errText string) error {
	repo.mu.Lock()
	defer repo.mu.Unlock()
	if id != repo.transfer.Id || repo.transfer.LastStatus != model.ScheduledTransferRunning || token != repo.transfer.LeaseToken {
		return errors.New("not running")
	}
	repo.finishN++
	repo.transfer.LastStatus = status
	repo.transfer.LastFinishAt = finishedAt
	repo.transfer.LastError = errText
	repo.transfer.LeaseUntil = time.Time{}
	repo.transfer.LeaseToken = ""
	return repo.finishErr
}

type runnerTestExecutor struct {
	run func(context.Context, model.ScheduledTransfer) error
}

func (executor runnerTestExecutor) Execute(ctx context.Context, transfer model.ScheduledTransfer) error {
	return executor.run(ctx, transfer)
}

func TestRunnerClaimsExecutesAndRecordsCompletion(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	now := time.Date(2026, time.October, 20, 8, 0, 0, 0, time.UTC)
	repo := &runnerTestRepository{transfer: model.ScheduledTransfer{
		Id: "transfer-1", Name: "backup", CronSpec: "* * * * *", Enabled: true,
		NextRunAt: now.Add(-time.Minute),
	}}
	var eventMu sync.Mutex
	var events []Event
	runner := Runner{
		Repository:   repo,
		Executor:     runnerTestExecutor{run: func(context.Context, model.ScheduledTransfer) error { return nil }},
		PollInterval: time.Millisecond, LeasePeriod: time.Minute, MaxConcurrent: 1,
		Now: func() time.Time { return now },
		OnEvent: func(event Event) {
			eventMu.Lock()
			events = append(events, event)
			eventMu.Unlock()
			if event.Phase == "finished" {
				cancel()
			}
		},
	}
	if err := runner.Run(ctx); err != nil {
		t.Fatal(err)
	}

	repo.mu.Lock()
	defer repo.mu.Unlock()
	if repo.claimN != 1 || repo.finishN != 1 {
		t.Fatalf("claims=%d finishes=%d; want one each", repo.claimN, repo.finishN)
	}
	if repo.transfer.LastStatus != model.ScheduledTransferSucceeded || !repo.transfer.LastFinishAt.Equal(now) {
		t.Fatalf("recorded transfer = %#v", repo.transfer)
	}
	if got, want := repo.transfer.NextRunAt, now.Add(time.Minute); !got.Equal(want) {
		t.Fatalf("next run = %s, want %s", got, want)
	}
	eventMu.Lock()
	defer eventMu.Unlock()
	if len(events) != 2 || events[0].Phase != "started" || events[1].Phase != "finished" || events[1].Status != model.ScheduledTransferSucceeded {
		t.Fatalf("events = %#v", events)
	}
}

func TestRunnerRecordsFailureAndBoundsErrorText(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	now := time.Now().UTC()
	repo := &runnerTestRepository{transfer: model.ScheduledTransfer{
		Id: "transfer-2", Name: "backup", CronSpec: "* * * * *", Enabled: true, NextRunAt: now.Add(-time.Minute),
	}}
	runner := Runner{
		Repository: repo,
		Executor: runnerTestExecutor{run: func(context.Context, model.ScheduledTransfer) error {
			return errors.New(strings.Repeat("x", maxStoredErrorRunes+10))
		}},
		PollInterval: time.Millisecond, MaxConcurrent: 1, Now: func() time.Time { return now },
		OnEvent: func(event Event) {
			if event.Phase == "finished" {
				cancel()
			}
		},
	}
	if err := runner.Run(ctx); err != nil {
		t.Fatal(err)
	}
	repo.mu.Lock()
	defer repo.mu.Unlock()
	if repo.transfer.LastStatus != model.ScheduledTransferFailed {
		t.Fatalf("status = %q; want failed", repo.transfer.LastStatus)
	}
	if len([]rune(repo.transfer.LastError)) != maxStoredErrorRunes {
		t.Fatalf("stored error length = %d; want %d", len([]rune(repo.transfer.LastError)), maxStoredErrorRunes)
	}
}

func TestRunnerExecutesImmediateRequestForDisabledTransferOnce(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	now := time.Date(2026, time.October, 20, 8, 0, 0, 0, time.UTC)
	planned := now.Add(24 * time.Hour)
	repo := &runnerTestRepository{transfer: model.ScheduledTransfer{
		Id: "transfer-now", Name: "one shot", CronSpec: "0 2 * * *", Enabled: false,
		RunRequested: true, NextRunAt: planned,
	}}
	var executed int
	runner := Runner{
		Repository: repo,
		Executor: runnerTestExecutor{run: func(_ context.Context, transfer model.ScheduledTransfer) error {
			executed++
			if !transfer.RunRequested || transfer.Enabled {
				t.Errorf("executor got transfer without manual-run state: %#v", transfer)
			}
			return nil
		}},
		PollInterval: time.Millisecond, LeasePeriod: time.Minute, MaxConcurrent: 1,
		Now: func() time.Time { return now },
		OnEvent: func(event Event) {
			if event.Phase == "finished" {
				cancel()
			}
		},
	}
	if err := runner.Run(ctx); err != nil {
		t.Fatal(err)
	}
	repo.mu.Lock()
	defer repo.mu.Unlock()
	if executed != 1 || repo.claimN != 1 || repo.finishN != 1 {
		t.Fatalf("manual execution count=%d claims=%d finishes=%d", executed, repo.claimN, repo.finishN)
	}
	if repo.transfer.Enabled || repo.transfer.RunRequested || !repo.transfer.NextRunAt.Equal(planned) {
		t.Fatalf("manual run changed recurring schedule: %#v", repo.transfer)
	}
}
