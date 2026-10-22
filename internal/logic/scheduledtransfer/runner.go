package scheduledtransfer

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"tui-ssh-workbench/internal/model"
)

const (
	DefaultPollInterval = 15 * time.Second
	DefaultLeasePeriod  = 2 * time.Minute
	DefaultConcurrency  = 3
	maxStoredErrorRunes = 2048
)

// Repository is the persistence surface needed by the background scheduler.
type Repository interface {
	ListDueScheduledTransfers(context.Context, time.Time, int) ([]model.ScheduledTransfer, error)
	ClaimScheduledTransfer(context.Context, string, string, bool, time.Time, time.Time, time.Time) (bool, error)
	RenewScheduledTransferLease(context.Context, string, string, time.Time) error
	FinishScheduledTransfer(context.Context, string, string, model.ScheduledTransferStatus, time.Time, string) error
}

// Executor performs one transfer using the saved plan and host credentials.
type Executor interface {
	Execute(context.Context, model.ScheduledTransfer) error
}

type Event struct {
	Transfer model.ScheduledTransfer
	Phase    string
	Status   model.ScheduledTransferStatus
	Err      error
}

// Runner polls for due plans, claims each plan in SQLite, and limits concurrent
// transfers. A claimed plan advances to its next future occurrence before the
// transfer begins; missed occurrences are coalesced into one run.
type Runner struct {
	Repository    Repository
	Executor      Executor
	PollInterval  time.Duration
	LeasePeriod   time.Duration
	MaxConcurrent int
	Now           func() time.Time
	OnEvent       func(Event)
}

func (r Runner) Run(ctx context.Context) error {
	if r.Repository == nil {
		return errors.New("scheduled transfer repository is not configured")
	}
	if r.Executor == nil {
		return errors.New("scheduled transfer executor is not configured")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if ctx.Err() != nil {
		return nil
	}
	if r.PollInterval <= 0 {
		r.PollInterval = DefaultPollInterval
	}
	if r.LeasePeriod < 30*time.Second {
		r.LeasePeriod = DefaultLeasePeriod
	}
	if r.MaxConcurrent < 1 || r.MaxConcurrent > 32 {
		r.MaxConcurrent = DefaultConcurrency
	}
	if r.Now == nil {
		r.Now = time.Now
	}

	workerContext, cancelWorkers := context.WithCancel(ctx)
	defer cancelWorkers()
	semaphore := make(chan struct{}, r.MaxConcurrent)
	var workers sync.WaitGroup
	ticker := time.NewTicker(r.PollInterval)
	defer ticker.Stop()

	for {
		if err := r.dispatch(workerContext, semaphore, &workers); err != nil {
			cancelWorkers()
			workers.Wait()
			return err
		}
		select {
		case <-ctx.Done():
			cancelWorkers()
			workers.Wait()
			return nil
		case <-ticker.C:
		}
	}
}

func (r Runner) dispatch(ctx context.Context, semaphore chan struct{}, workers *sync.WaitGroup) error {
	available := cap(semaphore) - len(semaphore)
	if available <= 0 {
		return nil
	}
	now := r.Now()
	due, err := r.Repository.ListDueScheduledTransfers(ctx, now, available)
	if err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return fmt.Errorf("list due scheduled transfers: %w", err)
	}
	for _, transfer := range due {
		if err := ctx.Err(); err != nil {
			return nil
		}
		nextRun := transfer.NextRunAt
		if !transfer.RunRequested {
			nextRun, err = NextRunInLocation(transfer.CronSpec, transfer.TimeZone, now)
			if err != nil {
				return fmt.Errorf("calculate next run for %q: %w", transfer.Name, err)
			}
		}
		leaseToken, err := newLeaseToken()
		if err != nil {
			return fmt.Errorf("create lease token for %q: %w", transfer.Name, err)
		}
		claimed, err := r.Repository.ClaimScheduledTransfer(
			ctx, transfer.Id, leaseToken, transfer.RunRequested, now, nextRun, now.Add(r.LeasePeriod),
		)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("claim scheduled transfer %q: %w", transfer.Name, err)
		}
		if !claimed {
			continue
		}
		semaphore <- struct{}{}
		workers.Add(1)
		r.emit(Event{Transfer: transfer, Phase: "started", Status: model.ScheduledTransferRunning})
		go func(transfer model.ScheduledTransfer) {
			defer workers.Done()
			defer func() { <-semaphore }()
			r.runClaimed(ctx, transfer, leaseToken)
		}(transfer)
	}
	return nil
}

func (r Runner) runClaimed(parent context.Context, transfer model.ScheduledTransfer, leaseToken string) {
	ctx, cancel := context.WithCancel(parent)
	leaseErr := make(chan error, 1)
	leaseStopped := make(chan struct{})
	go func() {
		defer close(leaseStopped)
		interval := r.LeasePeriod / 3
		if interval < 10*time.Second {
			interval = 10 * time.Second
		}
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := r.Repository.RenewScheduledTransferLease(ctx, transfer.Id, leaseToken, r.Now().Add(r.LeasePeriod)); err != nil {
					if ctx.Err() != nil {
						return
					}
					select {
					case leaseErr <- fmt.Errorf("renew lease: %w", err):
					default:
					}
					cancel()
					return
				}
			}
		}
	}()

	err := r.Executor.Execute(ctx, transfer)
	cancel()
	<-leaseStopped
	select {
	case renewErr := <-leaseErr:
		err = renewErr
	default:
	}

	status := model.ScheduledTransferSucceeded
	if err != nil {
		status = model.ScheduledTransferFailed
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			status = model.ScheduledTransferCanceled
		}
	}
	finishContext, finishCancel := context.WithTimeout(context.Background(), 10*time.Second)
	finishErr := r.Repository.FinishScheduledTransfer(finishContext, transfer.Id, leaseToken, status, r.Now(), boundedError(err))
	finishCancel()
	eventStatus := status
	if finishErr != nil {
		err = errors.Join(err, fmt.Errorf("record transfer result: %w", finishErr))
		eventStatus = model.ScheduledTransferFailed
	}
	r.emit(Event{Transfer: transfer, Phase: "finished", Status: eventStatus, Err: err})
}

func newLeaseToken() (string, error) {
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(token[:]), nil
}

func (r Runner) emit(event Event) {
	if r.OnEvent != nil {
		r.OnEvent(event)
	}
}

func boundedError(err error) string {
	if err == nil {
		return ""
	}
	text := strings.TrimSpace(err.Error())
	runes := []rune(text)
	if len(runes) > maxStoredErrorRunes {
		text = string(runes[:maxStoredErrorRunes])
	}
	return text
}
