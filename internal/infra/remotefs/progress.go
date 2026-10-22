package remotefs

import "context"

// TransferProgress describes byte and file-level progress for recursive
// transfers. CurrentPath is relative to the requested directory root.
type TransferProgress struct {
	CurrentPath    string
	FilesCompleted int
	FilesTotal     int
	Transferred    int64
	Total          int64
}

type progressReporterKey struct{}

// WithTransferProgressReporter attaches an optional detailed progress sink to
// a transfer context. Existing Client progress callbacks remain supported.
func WithTransferProgressReporter(ctx context.Context, reporter func(TransferProgress)) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if reporter == nil {
		return ctx
	}
	return context.WithValue(ctx, progressReporterKey{}, reporter)
}

// TransferProgressReporter returns the detailed progress sink currently
// attached to ctx. It lets a protocol adapter wrap progress with directory
// context without dropping the caller's reporter.
func TransferProgressReporter(ctx context.Context) func(TransferProgress) {
	if ctx == nil {
		return nil
	}
	reporter, _ := ctx.Value(progressReporterKey{}).(func(TransferProgress))
	return reporter
}

// ReportTransferProgress publishes file-level progress when the caller
// supplied a reporter. A missing reporter is intentionally a no-op.
func ReportTransferProgress(ctx context.Context, progress TransferProgress) {
	if ctx == nil {
		return
	}
	if reporter, ok := ctx.Value(progressReporterKey{}).(func(TransferProgress)); ok && reporter != nil {
		reporter(progress)
	}
}
