package sftpclient

import (
	"context"
	"sync"
)

// TransferPauseGate coordinates pause and resume across concurrent transfers.
// A paused transfer blocks at its next safe checkpoint and resumes without
// restarting the current file.
type TransferPauseGate struct {
	mu      sync.Mutex
	paused  bool
	resumed chan struct{}
}

// NewTransferPauseGate creates a gate that starts in the resumed state.
func NewTransferPauseGate() *TransferPauseGate {
	return &TransferPauseGate{}
}

// Pause prevents transfers from passing their next pause checkpoint.
func (g *TransferPauseGate) Pause() {
	if g == nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.paused {
		return
	}
	g.paused = true
	g.resumed = make(chan struct{})
}

// Resume releases transfers waiting at the pause gate.
func (g *TransferPauseGate) Resume() {
	if g == nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.paused {
		return
	}
	g.paused = false
	close(g.resumed)
	g.resumed = nil
}

// IsPaused reports whether the gate currently blocks transfers.
func (g *TransferPauseGate) IsPaused() bool {
	if g == nil {
		return false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.paused
}

// Wait blocks until the gate is resumed or ctx is cancelled.
func (g *TransferPauseGate) Wait(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if g == nil {
			return nil
		}
		g.mu.Lock()
		if !g.paused {
			g.mu.Unlock()
			return nil
		}
		resumed := g.resumed
		g.mu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-resumed:
		}
	}
}

type transferPauseContextKey struct{}

// WithTransferPauseGate attaches a pause gate to ctx for SFTP operations.
func WithTransferPauseGate(ctx context.Context, gate *TransferPauseGate) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if gate == nil {
		return ctx
	}
	return context.WithValue(ctx, transferPauseContextKey{}, gate)
}

// WaitIfTransferPaused waits at the pause gate carried by ctx, if any.
func WaitIfTransferPaused(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	gate, _ := ctx.Value(transferPauseContextKey{}).(*TransferPauseGate)
	return gate.Wait(ctx)
}
