package ui

import (
	"context"
	"testing"
	"time"

	"tui-ssh-workbench/internal/infra/sftpclient"
)

func TestSFTPTransferProgressHonorsPauseGate(t *testing.T) {
	gate := sftpclient.NewTransferPauseGate()
	gate.Pause()
	ctx := sftpclient.WithTransferPauseGate(context.Background(), gate)
	progress := make(chan sftpProgress, 1)
	started := make(chan struct{})
	go func() {
		close(started)
		sftpTransferProgress(ctx, progress, 0)(4, 10)
	}()
	<-started

	select {
	case got := <-progress:
		t.Fatalf("paused transfer reported progress early: %#v", got)
	case <-time.After(20 * time.Millisecond):
	}

	gate.Resume()
	select {
	case got := <-progress:
		if got.transferred != 4 || got.total != 10 {
			t.Fatalf("progress = %#v, want 4/10", got)
		}
	case <-time.After(time.Second):
		t.Fatal("transfer progress did not continue after resume")
	}
}
