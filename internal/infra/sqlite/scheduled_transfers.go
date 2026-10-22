package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	schedlogic "tui-ssh-workbench/internal/logic/scheduledtransfer"
	"tui-ssh-workbench/internal/model"
)

// ListScheduledTransfers returns every saved transfer plan in display order.
func (s *Store) ListScheduledTransfers(ctx context.Context) ([]model.ScheduledTransfer, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, name, host_id, direction, local_path, remote_path, cron_spec, time_zone,
		       enabled, overwrite, next_run_at, last_run_at, last_finish_at,
		       last_status, last_error, lease_until, lease_token, run_requested
		FROM scheduled_transfers
		ORDER BY name COLLATE NOCASE, id
	`)
	if err != nil {
		return nil, fmt.Errorf("list scheduled transfers: %w", err)
	}
	defer rows.Close()

	transfers := make([]model.ScheduledTransfer, 0)
	for rows.Next() {
		transfer, err := scanScheduledTransfer(rows)
		if err != nil {
			return nil, fmt.Errorf("scan scheduled transfer: %w", err)
		}
		transfers = append(transfers, transfer)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate scheduled transfers: %w", err)
	}
	return transfers, nil
}

// GetScheduledTransfer retrieves one saved plan.
func (s *Store) GetScheduledTransfer(ctx context.Context, id string) (model.ScheduledTransfer, error) {
	transfer, err := scanScheduledTransfer(s.db.QueryRowContext(ctx, `
		SELECT id, name, host_id, direction, local_path, remote_path, cron_spec, time_zone,
		       enabled, overwrite, next_run_at, last_run_at, last_finish_at,
		       last_status, last_error, lease_until, lease_token, run_requested
		FROM scheduled_transfers WHERE id = ?
	`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return model.ScheduledTransfer{}, ErrNotFound
	}
	if err != nil {
		return model.ScheduledTransfer{}, fmt.Errorf("get scheduled transfer: %w", err)
	}
	return transfer, nil
}

// SaveScheduledTransfer creates or updates a recurring transfer definition.
// Callers calculate NextRunAt with the schedule policy package before saving.
func (s *Store) SaveScheduledTransfer(ctx context.Context, transfer model.ScheduledTransfer) (model.ScheduledTransfer, error) {
	transfer.Name = strings.TrimSpace(transfer.Name)
	transfer.HostId = strings.TrimSpace(transfer.HostId)
	transfer.LocalPath = strings.TrimSpace(transfer.LocalPath)
	transfer.RemotePath = strings.TrimSpace(transfer.RemotePath)
	transfer.CronSpec = strings.TrimSpace(transfer.CronSpec)
	transfer.TimeZone = strings.TrimSpace(transfer.TimeZone)
	if transfer.TimeZone == "" {
		transfer.TimeZone = "Local"
	}
	if transfer.Name == "" || len([]rune(transfer.Name)) > 80 {
		return model.ScheduledTransfer{}, errors.New("计划任务名称不能为空且不能超过 80 个字符")
	}
	for _, char := range transfer.Name {
		if unicode.IsControl(char) {
			return model.ScheduledTransfer{}, errors.New("计划任务名称不能包含控制字符")
		}
	}
	if transfer.HostId == "" {
		return model.ScheduledTransfer{}, errors.New("计划任务必须选择主机")
	}
	if transfer.Direction != model.ScheduledTransferUpload && transfer.Direction != model.ScheduledTransferDownload {
		return model.ScheduledTransfer{}, errors.New("计划任务方向必须是上传或下载")
	}
	if transfer.LocalPath == "" || transfer.RemotePath == "" {
		return model.ScheduledTransfer{}, errors.New("本地路径和远程路径不能为空")
	}
	if err := schedlogic.ValidateSchedule(transfer.CronSpec); err != nil {
		return model.ScheduledTransfer{}, err
	}
	if err := schedlogic.ValidateTimeZone(transfer.TimeZone); err != nil {
		return model.ScheduledTransfer{}, err
	}
	if transfer.Enabled && transfer.NextRunAt.IsZero() {
		return model.ScheduledTransfer{}, errors.New("启用的计划任务必须有下次运行时间")
	}
	if transfer.Id == "" {
		id, err := newID()
		if err != nil {
			return model.ScheduledTransfer{}, fmt.Errorf("generate scheduled transfer ID: %w", err)
		}
		transfer.Id = id
	}
	host, err := s.GetHost(ctx, transfer.HostId)
	if err != nil {
		return model.ScheduledTransfer{}, fmt.Errorf("find transfer host: %w", err)
	}
	if host.Kind != model.HostKindSSH && host.Kind != model.HostKindFTP {
		return model.ScheduledTransfer{}, errors.New("计划传输只支持 SSH/SFTP、SSH/SCP 或 FTP/FTPS 主机")
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO scheduled_transfers (
			id, name, host_id, direction, local_path, remote_path, cron_spec, time_zone,
			enabled, overwrite, next_run_at, last_run_at, last_finish_at,
			last_status, last_error, lease_until
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, 0, 'idle', '', 0)
		ON CONFLICT(id) DO UPDATE SET
			name = excluded.name,
			host_id = excluded.host_id,
			direction = excluded.direction,
			local_path = excluded.local_path,
			remote_path = excluded.remote_path,
			cron_spec = excluded.cron_spec,
			time_zone = excluded.time_zone,
			enabled = excluded.enabled,
			overwrite = excluded.overwrite,
			next_run_at = excluded.next_run_at,
			lease_until = 0
		WHERE scheduled_transfers.last_status != 'running'
		  AND scheduled_transfers.run_requested = 0
	`, transfer.Id, transfer.Name, transfer.HostId, transfer.Direction,
		transfer.LocalPath, transfer.RemotePath, transfer.CronSpec, transfer.TimeZone,
		boolToInt(transfer.Enabled), boolToInt(transfer.Overwrite), timeToNanos(transfer.NextRunAt))
	if err != nil {
		return model.ScheduledTransfer{}, fmt.Errorf("save scheduled transfer: %w", err)
	}
	loaded, err := s.GetScheduledTransfer(ctx, transfer.Id)
	if err != nil {
		return model.ScheduledTransfer{}, err
	}
	if loaded.LastStatus == model.ScheduledTransferRunning || loaded.RunRequested {
		return model.ScheduledTransfer{}, errors.New("正在运行或等待立即执行的计划任务不能编辑")
	}
	return loaded, nil
}

// RequestScheduledTransferNow queues exactly one immediate execution without
// changing whether the recurring schedule is enabled or its next occurrence.
func (s *Store) RequestScheduledTransferNow(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, `
		UPDATE scheduled_transfers SET run_requested = 1
		WHERE id = ? AND last_status != 'running' AND run_requested = 0
	`, id)
	if err != nil {
		return fmt.Errorf("request immediate scheduled transfer: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("check immediate scheduled transfer request: %w", err)
	}
	if count == 1 {
		return nil
	}
	transfer, err := s.GetScheduledTransfer(ctx, id)
	if err != nil {
		return err
	}
	if transfer.LastStatus == model.ScheduledTransferRunning {
		return errors.New("计划任务正在运行")
	}
	if transfer.RunRequested {
		return errors.New("计划任务已经等待立即执行")
	}
	return errors.New("无法请求立即执行计划任务")
}

// DeleteScheduledTransfer removes a plan that is not currently running.
func (s *Store) DeleteScheduledTransfer(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM scheduled_transfers WHERE id = ? AND last_status != 'running'`, id)
	if err != nil {
		return fmt.Errorf("delete scheduled transfer: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("check deleted scheduled transfer: %w", err)
	}
	if count == 1 {
		return nil
	}
	transfer, err := s.GetScheduledTransfer(ctx, id)
	if err != nil {
		return err
	}
	if transfer.LastStatus == model.ScheduledTransferRunning {
		return errors.New("正在运行的计划任务不能删除")
	}
	return ErrNotFound
}

// ListDueScheduledTransfers returns plans whose schedule is due and whose
// previous execution lease has expired.
func (s *Store) ListDueScheduledTransfers(ctx context.Context, now time.Time, limit int) ([]model.ScheduledTransfer, error) {
	if limit < 1 {
		return []model.ScheduledTransfer{}, nil
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, name, host_id, direction, local_path, remote_path, cron_spec, time_zone,
		       enabled, overwrite, next_run_at, last_run_at, last_finish_at,
		       last_status, last_error, lease_until, lease_token, run_requested
		FROM scheduled_transfers
		WHERE run_requested = 1
		   OR (enabled = 1 AND (next_run_at <= ? OR (last_status = 'running' AND lease_until <= ?)))
		ORDER BY run_requested DESC, next_run_at, id
		LIMIT ?
	`, now.UnixNano(), now.UnixNano(), limit)
	if err != nil {
		return nil, fmt.Errorf("list due scheduled transfers: %w", err)
	}
	defer rows.Close()
	transfers := make([]model.ScheduledTransfer, 0)
	for rows.Next() {
		transfer, err := scanScheduledTransfer(rows)
		if err != nil {
			return nil, fmt.Errorf("scan due scheduled transfer: %w", err)
		}
		transfers = append(transfers, transfer)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate due scheduled transfers: %w", err)
	}
	return transfers, nil
}

// ClaimScheduledTransfer atomically claims one due plan and advances its next
// occurrence. A false result means another runner claimed it or it is no longer due.
func (s *Store) ClaimScheduledTransfer(ctx context.Context, id, leaseToken string, runRequested bool, now, nextRunAt, leaseUntil time.Time) (bool, error) {
	if strings.TrimSpace(leaseToken) == "" {
		return false, errors.New("scheduled transfer lease token is required")
	}
	result, err := s.db.ExecContext(ctx, `
		UPDATE scheduled_transfers
		SET next_run_at = CASE
				WHEN run_requested = 1 THEN next_run_at
				WHEN last_status = 'running' AND lease_until <= ? THEN next_run_at
				ELSE ?
			END,
			last_status = 'running', last_run_at = ?,
		    last_error = '', lease_until = ?, lease_token = ?, run_requested = 0
		WHERE id = ? AND (
			(run_requested = 1 AND ? = 1 AND last_status != 'running') OR
			(run_requested = 0 AND ? = 0 AND enabled = 1
			 AND (next_run_at <= ? OR (last_status = 'running' AND lease_until <= ?)))
		)
	`, now.UnixNano(), timeToNanos(nextRunAt), now.UnixNano(), leaseUntil.UnixNano(), leaseToken, id,
		boolToInt(runRequested), boolToInt(runRequested), now.UnixNano(), now.UnixNano())
	if err != nil {
		return false, fmt.Errorf("claim scheduled transfer: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("check scheduled transfer claim: %w", err)
	}
	return count == 1, nil
}

// RenewScheduledTransferLease extends the ownership window for a long transfer.
func (s *Store) RenewScheduledTransferLease(ctx context.Context, id, leaseToken string, leaseUntil time.Time) error {
	result, err := s.db.ExecContext(ctx, `
		UPDATE scheduled_transfers SET lease_until = ?
		WHERE id = ? AND last_status = 'running' AND lease_token = ?
	`, leaseUntil.UnixNano(), id, leaseToken)
	if err != nil {
		return fmt.Errorf("renew scheduled transfer lease: %w", err)
	}
	return requireAffected(result, "renew scheduled transfer lease")
}

// FinishScheduledTransfer records an execution result and releases its lease.
func (s *Store) FinishScheduledTransfer(ctx context.Context, id, leaseToken string, status model.ScheduledTransferStatus, finishedAt time.Time, errText string) error {
	if status != model.ScheduledTransferSucceeded && status != model.ScheduledTransferFailed && status != model.ScheduledTransferCanceled {
		return errors.New("invalid scheduled transfer finish status")
	}
	result, err := s.db.ExecContext(ctx, `
		UPDATE scheduled_transfers
		SET last_status = ?, last_finish_at = ?, last_error = ?, lease_until = 0, lease_token = ''
		WHERE id = ? AND last_status = 'running' AND lease_token = ?
	`, status, finishedAt.UnixNano(), errText, id, leaseToken)
	if err != nil {
		return fmt.Errorf("finish scheduled transfer: %w", err)
	}
	return requireAffected(result, "finish scheduled transfer")
}

type scheduledTransferScanner interface {
	Scan(dest ...any) error
}

func scanScheduledTransfer(scanner scheduledTransferScanner) (model.ScheduledTransfer, error) {
	var transfer model.ScheduledTransfer
	var enabled, overwrite, runRequested int
	var nextRunAt, lastRunAt, lastFinishAt, leaseUntil int64
	err := scanner.Scan(
		&transfer.Id, &transfer.Name, &transfer.HostId, &transfer.Direction,
		&transfer.LocalPath, &transfer.RemotePath, &transfer.CronSpec, &transfer.TimeZone,
		&enabled, &overwrite, &nextRunAt, &lastRunAt, &lastFinishAt,
		&transfer.LastStatus, &transfer.LastError, &leaseUntil, &transfer.LeaseToken, &runRequested,
	)
	if err != nil {
		return model.ScheduledTransfer{}, err
	}
	transfer.Enabled, transfer.Overwrite = enabled != 0, overwrite != 0
	transfer.RunRequested = runRequested != 0
	transfer.NextRunAt = nanosToTime(nextRunAt)
	transfer.LastRunAt = nanosToTime(lastRunAt)
	transfer.LastFinishAt = nanosToTime(lastFinishAt)
	transfer.LeaseUntil = nanosToTime(leaseUntil)
	return transfer, nil
}

func timeToNanos(value time.Time) int64 {
	if value.IsZero() {
		return 0
	}
	return value.UnixNano()
}

func nanosToTime(value int64) time.Time {
	if value == 0 {
		return time.Time{}
	}
	return time.Unix(0, value)
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
