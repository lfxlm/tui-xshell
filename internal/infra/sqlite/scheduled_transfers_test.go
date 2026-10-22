package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"tui-ssh-workbench/internal/model"
)

func TestScheduledTransfersCRUDAndLeaseLifecycle(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "scheduled.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	host, err := store.CreateHost(ctx, model.Host{
		Kind: model.HostKindSSH, Name: "Build", IP: "192.0.2.10", Port: 22, User: "tester",
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	transfer, err := store.SaveScheduledTransfer(ctx, model.ScheduledTransfer{
		Name: "  nightly upload  ", HostId: host.Id,
		Direction: model.ScheduledTransferUpload,
		LocalPath: "/tmp/build.tar", RemotePath: "/incoming/build.tar",
		CronSpec: "0 2 * * *", TimeZone: "Asia/Shanghai", Enabled: true, NextRunAt: now.Add(-time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	if transfer.Id == "" || transfer.Name != "nightly upload" || transfer.LastStatus != model.ScheduledTransferIdle || transfer.TimeZone != "Asia/Shanghai" {
		t.Fatalf("saved transfer = %#v", transfer)
	}
	listed, err := store.ListScheduledTransfers(ctx)
	if err != nil || len(listed) != 1 || listed[0].Id != transfer.Id {
		t.Fatalf("listed transfers = %#v, err=%v", listed, err)
	}
	due, err := store.ListDueScheduledTransfers(ctx, now, 10)
	if err != nil || len(due) != 1 || due[0].Id != transfer.Id {
		t.Fatalf("due transfers = %#v, err=%v", due, err)
	}

	leaseUntil := now.Add(time.Minute)
	claimed, err := store.ClaimScheduledTransfer(ctx, transfer.Id, "worker-one", false, now, now.Add(24*time.Hour), leaseUntil)
	if err != nil || !claimed {
		t.Fatalf("claim = %t, err=%v; want true", claimed, err)
	}
	claimed, err = store.ClaimScheduledTransfer(ctx, transfer.Id, "worker-two", false, now, now.Add(48*time.Hour), now.Add(2*time.Minute))
	if err != nil || claimed {
		t.Fatalf("second claim = %t, err=%v; want false", claimed, err)
	}
	if err := store.DeleteScheduledTransfer(ctx, transfer.Id); err == nil {
		t.Fatal("deleted a running transfer")
	}
	if err := store.RenewScheduledTransferLease(ctx, transfer.Id, "worker-one", now.Add(2*time.Minute)); err != nil {
		t.Fatalf("renew lease: %v", err)
	}

	// An expired runner can be recovered immediately, even though the original
	// claim already advanced next_run_at to a future occurrence.
	due, err = store.ListDueScheduledTransfers(ctx, now.Add(3*time.Minute), 10)
	if err != nil || len(due) != 1 {
		t.Fatalf("expired lease due transfers = %#v, err=%v", due, err)
	}
	reclaimed, err := store.ClaimScheduledTransfer(ctx, transfer.Id, "worker-two", false, now.Add(3*time.Minute), now.Add(72*time.Hour), now.Add(4*time.Minute))
	if err != nil || !reclaimed {
		t.Fatalf("reclaim = %t, err=%v; want true", reclaimed, err)
	}
	loaded, err := store.GetScheduledTransfer(ctx, transfer.Id)
	if err != nil || !loaded.NextRunAt.Equal(now.Add(24*time.Hour)) {
		t.Fatalf("reclaimed transfer next run = %s, err=%v; want original advanced occurrence %s", loaded.NextRunAt, err, now.Add(24*time.Hour))
	}
	if err := store.RenewScheduledTransferLease(ctx, transfer.Id, "worker-one", now.Add(5*time.Minute)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("old worker lease renewal error = %v, want ErrNotFound", err)
	}
	if err := store.FinishScheduledTransfer(ctx, transfer.Id, "worker-one", model.ScheduledTransferSucceeded, now.Add(3*time.Minute), ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("old worker finish error = %v, want ErrNotFound", err)
	}

	if err := store.FinishScheduledTransfer(ctx, transfer.Id, "worker-two", model.ScheduledTransferSucceeded, now.Add(3*time.Minute), ""); err != nil {
		t.Fatalf("finish transfer: %v", err)
	}
	loaded, err = store.GetScheduledTransfer(ctx, transfer.Id)
	if err != nil || loaded.LastStatus != model.ScheduledTransferSucceeded || !loaded.LeaseUntil.IsZero() {
		t.Fatalf("finished transfer = %#v, err=%v", loaded, err)
	}
	if err := store.DeleteScheduledTransfer(ctx, transfer.Id); err != nil {
		t.Fatalf("delete finished transfer: %v", err)
	}
	if err := store.DeleteScheduledTransfer(ctx, transfer.Id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete missing transfer error = %v, want ErrNotFound", err)
	}
}

func TestScheduledTransferAcceptsSCPHostConfiguration(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "scheduled-scp.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	host, err := store.CreateHost(ctx, model.Host{
		Kind: model.HostKindSSH, FileProtocol: model.SSHFileProtocolSCP,
		Name: "legacy files", IP: "192.0.2.30", Port: 22, User: "tester",
	})
	if err != nil {
		t.Fatal(err)
	}
	transfer, err := store.SaveScheduledTransfer(ctx, model.ScheduledTransfer{
		Name: "scp upload", HostId: host.Id, Direction: model.ScheduledTransferUpload,
		LocalPath: "/tmp/archive.tar", RemotePath: "/incoming/archive.tar",
		CronSpec: "0 2 * * *", Enabled: false,
	})
	if err != nil || transfer.HostId != host.Id {
		t.Fatalf("save scheduled SCP transfer = %#v, err=%v", transfer, err)
	}
}

func TestScheduledTransferCanRunNowWithoutChangingDisabledSchedule(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "scheduled-now.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	host, err := store.CreateHost(ctx, model.Host{
		Kind: model.HostKindSSH, Name: "Build", IP: "192.0.2.12", Port: 22, User: "tester",
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	planned := now.Add(24 * time.Hour)
	transfer, err := store.SaveScheduledTransfer(ctx, model.ScheduledTransfer{
		Name: "disabled archive", HostId: host.Id, Direction: model.ScheduledTransferDownload,
		LocalPath: "/tmp/archive.tar", RemotePath: "/var/archive.tar",
		CronSpec: "0 2 * * *", Enabled: false, NextRunAt: time.Time{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if due, err := store.ListDueScheduledTransfers(ctx, now, 10); err != nil || len(due) != 0 {
		t.Fatalf("disabled task was initially due: %#v, err=%v", due, err)
	}
	if err := store.RequestScheduledTransferNow(ctx, transfer.Id); err != nil {
		t.Fatalf("request immediate run: %v", err)
	}
	if err := store.RequestScheduledTransferNow(ctx, transfer.Id); err == nil {
		t.Fatal("duplicate immediate run request was accepted")
	}
	loaded, err := store.GetScheduledTransfer(ctx, transfer.Id)
	if err != nil || !loaded.RunRequested || loaded.Enabled {
		t.Fatalf("queued disabled transfer = %#v, err=%v", loaded, err)
	}
	due, err := store.ListDueScheduledTransfers(ctx, now, 10)
	if err != nil || len(due) != 1 || !due[0].RunRequested {
		t.Fatalf("immediate transfer due list = %#v, err=%v", due, err)
	}
	if _, err := store.SaveScheduledTransfer(ctx, loaded); err == nil {
		t.Fatal("edited a task while an immediate execution is queued")
	}
	claimed, err := store.ClaimScheduledTransfer(ctx, transfer.Id, "worker-now", true, now, planned, now.Add(time.Minute))
	if err != nil || !claimed {
		t.Fatalf("claim immediate run = %t, err=%v", claimed, err)
	}
	loaded, err = store.GetScheduledTransfer(ctx, transfer.Id)
	if err != nil || loaded.RunRequested || loaded.Enabled || !loaded.NextRunAt.IsZero() {
		t.Fatalf("claimed one-shot execution changed recurring schedule: %#v, err=%v", loaded, err)
	}
	if err := store.RequestScheduledTransferNow(ctx, transfer.Id); err == nil {
		t.Fatal("requested another run while the task is running")
	}
}

func TestScheduledTransfersValidationAndHostDeletionGuard(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "scheduled-validation.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	host, err := store.CreateHost(ctx, model.Host{
		Kind: model.HostKindSSH, Name: "Build", IP: "192.0.2.11", Port: 22, User: "tester",
	})
	if err != nil {
		t.Fatal(err)
	}
	base := model.ScheduledTransfer{
		Name: "nightly", HostId: host.Id, Direction: model.ScheduledTransferDownload,
		LocalPath: "/tmp/archive.tar", RemotePath: "/var/archive.tar",
		CronSpec: "0 2 * * *", Enabled: true, NextRunAt: time.Now().Add(time.Hour),
	}
	invalid := base
	invalid.CronSpec = "not a schedule"
	if _, err := store.SaveScheduledTransfer(ctx, invalid); err == nil {
		t.Fatal("saved transfer with invalid schedule")
	}
	invalid = base
	invalid.HostId = "missing-host"
	if _, err := store.SaveScheduledTransfer(ctx, invalid); err == nil {
		t.Fatal("saved transfer for missing host")
	}
	invalid = base
	invalid.TimeZone = "Not/A_Real_Zone"
	if _, err := store.SaveScheduledTransfer(ctx, invalid); err == nil {
		t.Fatal("saved transfer with invalid timezone")
	}
	if _, err := store.SaveScheduledTransfer(ctx, base); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteHost(ctx, host.Id); !errors.Is(err, ErrHostHasScheduledTransfers) {
		t.Fatalf("delete host with scheduled transfer error = %v, want ErrHostHasScheduledTransfers", err)
	}
	transfers, err := store.ListScheduledTransfers(ctx)
	if err != nil || len(transfers) != 1 {
		t.Fatalf("transfers after rejected host deletion = %#v, err=%v; want task preserved", transfers, err)
	}
	if err := store.DeleteScheduledTransfer(ctx, transfers[0].Id); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteHost(ctx, host.Id); err != nil {
		t.Fatalf("delete host after its plans are removed: %v", err)
	}
}

func TestVersionSevenDatabaseMigratesScheduledTransfers(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "v7.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `DROP TABLE scheduled_transfers`); err != nil {
		t.Fatal(err)
	}
	dropRecentHostMetadataForLegacyFixture(t, ctx, db)
	if _, err := db.ExecContext(ctx, `PRAGMA user_version = 7`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(ctx, path)
	if err != nil {
		t.Fatalf("migrate v7 database: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.ListScheduledTransfers(ctx); err != nil {
		t.Fatalf("list after v7 migration: %v", err)
	}
	var version int
	if err := store.db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil || version != schemaVersion {
		t.Fatalf("migrated schema version = %d, err=%v; want %d", version, err, schemaVersion)
	}
}

func TestVersionTenDatabaseMigratesTimezoneWithLocalDefault(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "v10.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	host, err := store.CreateHost(ctx, model.Host{
		Kind: model.HostKindSSH, Name: "Build", IP: "192.0.2.13", Port: 22, User: "tester",
	})
	if err != nil {
		t.Fatal(err)
	}
	transfer, err := store.SaveScheduledTransfer(ctx, model.ScheduledTransfer{
		Name: "legacy schedule", HostId: host.Id, Direction: model.ScheduledTransferUpload,
		LocalPath: "/tmp/archive.tar", RemotePath: "/var/archive.tar", CronSpec: "0 2 * * *",
		TimeZone: "Asia/Shanghai", Enabled: true, NextRunAt: time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `ALTER TABLE scheduled_transfers DROP COLUMN time_zone`); err != nil {
		t.Fatalf("prepare version 10 schema: %v", err)
	}
	dropRecentHostMetadataForLegacyFixture(t, ctx, db)
	if _, err := db.ExecContext(ctx, `PRAGMA user_version = 10`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(ctx, path)
	if err != nil {
		t.Fatalf("migrate version 10 database: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	migrated, err := store.GetScheduledTransfer(ctx, transfer.Id)
	if err != nil || migrated.TimeZone != "Local" {
		t.Fatalf("migrated schedule timezone = %q, err=%v; want Local", migrated.TimeZone, err)
	}
	migratedHost, err := store.GetHost(ctx, host.Id)
	if err != nil || migratedHost.Favorite || !migratedHost.LastConnectedAt.IsZero() {
		t.Fatalf("migrated legacy host metadata = favorite:%v recent:%s err:%v; want false and unset", migratedHost.Favorite, migratedHost.LastConnectedAt, err)
	}
}
