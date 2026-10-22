package ui

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"tui-ssh-workbench/internal/infra/sqlite"
	"tui-ssh-workbench/internal/model"
)

func TestScheduledTransferRunnerFailureIsShownInStatus(t *testing.T) {
	state := modelState{width: 100, height: 30}
	updated, _ := state.Update(ScheduledTransferRunnerFailed(errors.New("数据库只读")))
	state = unwrapModel(t, updated)
	if state.statusText != "计划传输后台停止：数据库只读" || !state.statusErr {
		t.Fatalf("runner failure status = %q (error=%t)", state.statusText, state.statusErr)
	}
}

type fakeDesktopNotifier struct {
	title string
	body  string
	err   error
}

func (notifier *fakeDesktopNotifier) Notify(title, body string) error {
	notifier.title, notifier.body = title, body
	return notifier.err
}

func TestTransferNotificationCanSendDesktopNotification(t *testing.T) {
	notifier := &fakeDesktopNotifier{}
	state := modelState{
		config:          model.Config{Notifications: model.NotificationsConfig{Enabled: true}},
		desktopNotifier: notifier,
	}
	command := state.showTransferNotification("目录上传完成", false)
	if command == nil {
		t.Fatal("transfer completion did not schedule notifications")
	}
	batch, ok := command().(tea.BatchMsg)
	if !ok || len(batch) != 2 {
		t.Fatalf("notification command batch = %#v, want timer and desktop notification", batch)
	}
	if message := batch[1](); message != nil {
		t.Fatalf("desktop notification command returned unexpected message: %#v", message)
	}
	if notifier.title != "文件传输完成" || notifier.body != "目录上传完成" {
		t.Fatalf("desktop notification = %q / %q", notifier.title, notifier.body)
	}
	if state.scheduledTransferNotice != "目录上传完成" {
		t.Fatalf("in-app notice missing: %q", state.scheduledTransferNotice)
	}
}

func TestScheduledTransferPollNotifiesOnceForNewCompletion(t *testing.T) {
	startedAt := time.Now().Add(-time.Minute)
	finishedBeforeStart := startedAt.Add(-time.Minute)
	state := modelState{width: 120, height: 32, scheduledTransfersStartedAt: startedAt}
	initial := []model.ScheduledTransfer{{
		Id: "daily-backup", Name: "每日备份", LastStatus: model.ScheduledTransferRunning,
		LastFinishAt: finishedBeforeStart,
	}}
	if command := state.handleScheduledTransfersPolled(initial); command != nil || state.scheduledTransferNotice != "" {
		t.Fatalf("initial snapshot unexpectedly notified: command=%v notice=%q", command != nil, state.scheduledTransferNotice)
	}

	finishedAt := startedAt.Add(time.Second)
	completed := []model.ScheduledTransfer{{
		Id: "daily-backup", Name: "每日备份", LastStatus: model.ScheduledTransferSucceeded,
		LastFinishAt: finishedAt,
	}}
	if command := state.handleScheduledTransfersPolled(completed); command == nil {
		t.Fatal("new completion did not schedule a notice timeout")
	}
	if state.scheduledTransferNotice != "计划传输成功：每日备份" || state.scheduledTransferNoticeErr {
		t.Fatalf("success notice = %q, error=%v", state.scheduledTransferNotice, state.scheduledTransferNoticeErr)
	}
	if view := state.View().Content; !strings.Contains(view, state.scheduledTransferNotice) {
		t.Fatalf("global view does not display the completion notice:\n%s", view)
	}
	if command := state.handleScheduledTransfersPolled(completed); command != nil {
		t.Fatal("unchanged completion generated a duplicate notice")
	}
}

func TestScheduledTransferPollWarnsOnFailureAndIgnoresStaleTimeout(t *testing.T) {
	startedAt := time.Now().Add(-time.Minute)
	state := modelState{
		scheduledTransfersStartedAt: startedAt,
		scheduledTransferLastFinish: map[string]time.Time{"daily-backup": startedAt.Add(time.Second)},
	}
	failedAt := startedAt.Add(2 * time.Second)
	failed := []model.ScheduledTransfer{{
		Id: "daily-backup", Name: "每日备份", LastStatus: model.ScheduledTransferFailed,
		LastFinishAt: failedAt,
	}}
	if command := state.handleScheduledTransfersPolled(failed); command == nil {
		t.Fatal("failed completion did not schedule a notice timeout")
	}
	if state.scheduledTransferNotice != "计划传输失败：每日备份" || !state.scheduledTransferNoticeErr {
		t.Fatalf("failure notice = %q, error=%v", state.scheduledTransferNotice, state.scheduledTransferNoticeErr)
	}
	firstNoticeID := state.scheduledTransferNoticeID

	second := []model.ScheduledTransfer{
		failed[0],
		{Id: "weekly-export", Name: "每周导出", LastStatus: model.ScheduledTransferSucceeded, LastFinishAt: failedAt.Add(time.Second)},
	}
	state.handleScheduledTransfersPolled(second)
	if state.scheduledTransferNoticeID == firstNoticeID {
		t.Fatal("second completion did not replace the current notice")
	}
	updated, _ := state.Update(scheduledTransferNoticeExpiredMsg{id: firstNoticeID})
	state = unwrapModel(t, updated)
	if state.scheduledTransferNotice == "" {
		t.Fatal("stale timeout cleared a newer notice")
	}
	updated, _ = state.Update(scheduledTransferNoticeExpiredMsg{id: state.scheduledTransferNoticeID})
	state = unwrapModel(t, updated)
	if state.scheduledTransferNotice != "" || state.scheduledTransferNoticeErr {
		t.Fatalf("current timeout did not clear the notice: %q (id=%d)", state.scheduledTransferNotice, state.scheduledTransferNoticeID)
	}
}

func TestSFTPTransferCompletionUsesGlobalNotice(t *testing.T) {
	state := modelState{width: 120, height: 32, sftp: &sftpBrowserState{
		requestID: 9,
	}}
	command := state.handleSFTPOperation(sftpOperationMsg{
		requestID: 9,
		status:    "目录上传完成",
	})
	if command == nil || state.scheduledTransferNotice != "目录上传完成" || state.scheduledTransferNoticeErr {
		t.Fatalf("single transfer notice = %q, error=%v, timer=%v", state.scheduledTransferNotice, state.scheduledTransferNoticeErr, command != nil)
	}

	state = modelState{width: 120, height: 32, sftp: &sftpBrowserState{
		queueActive: true,
		queueTotal:  2,
		queueDone:   1,
	}}
	command = state.finishSFTPQueue([]sftpQueueResult{{transfer: sftpTransferRequest{}}}, nil)
	if command == nil || state.scheduledTransferNotice != "文件传输队列完成：2/2 成功" || state.scheduledTransferNoticeErr {
		t.Fatalf("queue notice = %q, error=%v, timer=%v", state.scheduledTransferNotice, state.scheduledTransferNoticeErr, command != nil)
	}
}

func TestScheduledTransferPollReadsRunnerResultFromSQLite(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "scheduled-notice.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	now := time.Now().UTC()
	host, err := store.CreateHost(ctx, model.Host{
		Kind: model.HostKindSSH, Name: "通知测试主机", IP: "127.0.0.1", Port: 22, User: "tester",
	})
	if err != nil {
		t.Fatal(err)
	}
	transfer, err := store.SaveScheduledTransfer(ctx, model.ScheduledTransfer{
		Name: "夜间备份", HostId: host.Id, Direction: model.ScheduledTransferUpload,
		LocalPath: "/tmp/archive.tar", RemotePath: "/backup/archive.tar",
		CronSpec: "@every 1h", TimeZone: "UTC", Enabled: true, NextRunAt: now.Add(-time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := store.ClaimScheduledTransfer(ctx, transfer.Id, "runner-token", false,
		now, now.Add(time.Hour), now.Add(time.Minute))
	if err != nil || !claimed {
		t.Fatalf("claim scheduled task = %t, err=%v", claimed, err)
	}
	finishedAt := now.Add(time.Second)
	if err := store.FinishScheduledTransfer(ctx, transfer.Id, "runner-token", model.ScheduledTransferSucceeded, finishedAt, ""); err != nil {
		t.Fatal(err)
	}

	state := modelState{
		catalog: store, width: 120, height: 32,
		scheduledTransfersStartedAt: now,
	}
	message := state.pollScheduledTransfersCmd()().(scheduledTransfersPolledMsg)
	updated, _ := state.Update(message)
	state = unwrapModel(t, updated)
	if state.scheduledTransferNotice != "计划传输成功：夜间备份" || state.scheduledTransferNoticeErr {
		t.Fatalf("SQLite runner result notice = %q, error=%v", state.scheduledTransferNotice, state.scheduledTransferNoticeErr)
	}
}
