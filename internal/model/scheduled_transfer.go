package model

import "time"

type ScheduledTransferDirection string

const (
	ScheduledTransferUpload   ScheduledTransferDirection = "upload"
	ScheduledTransferDownload ScheduledTransferDirection = "download"
)

type ScheduledTransferStatus string

const (
	ScheduledTransferIdle      ScheduledTransferStatus = "idle"
	ScheduledTransferRunning   ScheduledTransferStatus = "running"
	ScheduledTransferSucceeded ScheduledTransferStatus = "succeeded"
	ScheduledTransferFailed    ScheduledTransferStatus = "failed"
	ScheduledTransferCanceled  ScheduledTransferStatus = "canceled"
)

// ScheduledTransfer describes a recurring local/remote transfer. Secrets are
// resolved from the host's system-keyring reference at run time.
type ScheduledTransfer struct {
	Id           string                     `json:"id"`
	Name         string                     `json:"name"`
	HostId       string                     `json:"host_id"`
	Direction    ScheduledTransferDirection `json:"direction"`
	LocalPath    string                     `json:"local_path"`
	RemotePath   string                     `json:"remote_path"`
	CronSpec     string                     `json:"cron_spec"`
	TimeZone     string                     `json:"time_zone"`
	Enabled      bool                       `json:"enabled"`
	Overwrite    bool                       `json:"overwrite"`
	RunRequested bool                       `json:"run_requested,omitempty"`
	NextRunAt    time.Time                  `json:"next_run_at"`
	LastRunAt    time.Time                  `json:"last_run_at,omitempty"`
	LastFinishAt time.Time                  `json:"last_finish_at,omitempty"`
	LastStatus   ScheduledTransferStatus    `json:"last_status"`
	LastError    string                     `json:"last_error,omitempty"`
	LeaseUntil   time.Time                  `json:"-"`
	LeaseToken   string                     `json:"-"`
}
