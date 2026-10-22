package sftpclient

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync/atomic"
	"testing"
	"time"
)

func TestTransferPauseGatePausesCopyAndResumes(t *testing.T) {
	gate := NewTransferPauseGate()
	gate.Pause()
	ctx := WithTransferPauseGate(context.Background(), gate)
	var written atomic.Int64
	done := make(chan error, 1)
	go func() {
		done <- copyWithProgress(ctx, countingWriter{written: &written}, bytes.NewReader(bytes.Repeat([]byte("x"), 128*1024)), 128*1024, nil)
	}()

	select {
	case err := <-done:
		t.Fatalf("copy completed while paused: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	if got := written.Load(); got != 0 {
		t.Fatalf("bytes written while paused = %d, want 0", got)
	}

	gate.Resume()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("copy after resume: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("copy did not resume")
	}
	if got := written.Load(); got != 128*1024 {
		t.Fatalf("bytes written after resume = %d, want %d", got, 128*1024)
	}
}

func TestTransferPauseGateCancellationUnblocksWait(t *testing.T) {
	gate := NewTransferPauseGate()
	gate.Pause()
	ctx, cancel := context.WithCancel(WithTransferPauseGate(context.Background(), gate))
	done := make(chan error, 1)
	go func() { done <- WaitIfTransferPaused(ctx) }()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("wait error = %v, want context canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled wait remained blocked")
	}
}

type countingWriter struct{ written *atomic.Int64 }

func (w countingWriter) Write(data []byte) (int, error) {
	w.written.Add(int64(len(data)))
	return len(data), nil
}

var _ io.Writer = countingWriter{}
