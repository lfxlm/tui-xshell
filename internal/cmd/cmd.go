package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gogf/gf/v2/os/gcmd"

	"tui-ssh-workbench/internal/app"
	"tui-ssh-workbench/internal/infra/desktopnotify"
	"tui-ssh-workbench/internal/infra/sessionfile"
	"tui-ssh-workbench/internal/infra/sqlite"
	"tui-ssh-workbench/internal/logic/scheduledtransfer"
	"tui-ssh-workbench/internal/model"
	"tui-ssh-workbench/internal/service"
)

var Main = gcmd.Command{
	Name:  "tui-ssh-workbench",
	Usage: "tui-ssh-workbench [OPTION]",
	Brief: "Start the terminal SSH workbench",
	Examples: `
tui-ssh-workbench
tui-ssh-workbench --export-sessions ./sessions.json
tui-ssh-workbench --import-sessions ./sessions.json
tui-ssh-workbench --db ./workbench.db --export-sessions ./sessions.json
tui-ssh-workbench --export-xshell ./xshell-sessions
tui-ssh-workbench --export-xftp ./xftp-sessions
tui-ssh-workbench --run-scheduled
tui-ssh-workbench --transfer --transfer-host prod --transfer-direction upload --transfer-local ./app.tar.gz --transfer-remote /srv/app.tar.gz
`,
	Arguments: []gcmd.Argument{
		{Name: "config", Brief: "configuration file path"},
		{Name: "db", Brief: "SQLite database path"},
		{Name: "export-sessions", Brief: "export saved sessions to a new JSON file"},
		{Name: "export-xshell", Brief: "export supported saved sessions to a new Xshell session directory"},
		{Name: "export-xftp", Brief: "export supported saved sessions to a new Xftp session directory"},
		{Name: "import-sessions", Brief: "import sessions from JSON, Xshell .xsh, Xftp .xfp, or a native session directory"},
		{Name: "run-scheduled", Brief: "持续运行已保存的计划传输任务", Orphan: true},
		{Name: "transfer", Brief: "传输本地文件或目录", Orphan: true},
		{Name: "transfer-host", Brief: "主机 ID 或唯一名称"},
		{Name: "transfer-direction", Brief: "upload 或 download"},
		{Name: "transfer-local", Brief: "本地文件或目录路径"},
		{Name: "transfer-remote", Brief: "远程文件或目录路径"},
		{Name: "transfer-overwrite", Brief: "覆盖已存在的目标", Orphan: true},
		{Name: "help", Short: "h", Brief: "show this help", Orphan: true},
	},
	Func: func(ctx context.Context, parser *gcmd.Parser) (err error) {
		if err = service.ConfigManager().ParseConfig(ctx, parser); err != nil {
			return err
		}

		databasePath := parser.GetOpt("db", "").String()
		exportPath := strings.TrimSpace(parser.GetOpt("export-sessions", "").String())
		exportXShellPath := strings.TrimSpace(parser.GetOpt("export-xshell", "").String())
		exportXFTPPath := strings.TrimSpace(parser.GetOpt("export-xftp", "").String())
		importPath := strings.TrimSpace(parser.GetOpt("import-sessions", "").String())
		runScheduledOption := parser.GetOpt("run-scheduled")
		runScheduled := runScheduledOption != nil && (runScheduledOption.String() == "" || runScheduledOption.Bool())
		transferOption := parser.GetOpt("transfer")
		transferRequested := transferOption != nil && (transferOption.String() == "" || transferOption.Bool())
		transferHost := strings.TrimSpace(parser.GetOpt("transfer-host", "").String())
		transferDirection := strings.TrimSpace(parser.GetOpt("transfer-direction", "").String())
		transferLocal := parser.GetOpt("transfer-local", "").String()
		transferRemote := parser.GetOpt("transfer-remote", "").String()
		transferOverwriteOption := parser.GetOpt("transfer-overwrite")
		transferOverwrite := transferOverwriteOption != nil && (transferOverwriteOption.String() == "" || transferOverwriteOption.Bool())
		transferParameters := transferHost != "" || transferDirection != "" || transferLocal != "" || transferRemote != "" || transferOverwriteOption != nil
		if runScheduled {
			if exportPath != "" || exportXShellPath != "" || exportXFTPPath != "" || importPath != "" || transferRequested || transferParameters {
				return errors.New("--run-scheduled 不能与会话导入、导出或单次传输同时使用")
			}
			signalContext, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
			defer stop()
			return app.RunScheduledTransfers(signalContext, databasePath, service.ConfigManager().Config(), printScheduledTransferEvent)
		}
		if transferParameters && !transferRequested {
			return errors.New("传输参数必须与 --transfer 一起使用")
		}
		if transferRequested {
			if exportPath != "" || exportXShellPath != "" || exportXFTPPath != "" || importPath != "" {
				return errors.New("--transfer 不能与会话导入或导出同时使用")
			}
			if transferDirection != string(model.ScheduledTransferUpload) && transferDirection != string(model.ScheduledTransferDownload) {
				return errors.New("--transfer-direction 必须是 upload 或 download")
			}
			signalContext, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
			defer stop()
			_, _ = fmt.Fprintf(os.Stdout, "开始%s传输：%s → %s\n", transferDirection, transferLocal, transferRemote)
			progress := &cliTransferProgress{}
			err := app.RunFileTransfer(signalContext, databasePath, service.ConfigManager().Config(),
				transferHost, model.ScheduledTransferDirection(transferDirection), transferLocal, transferRemote,
				transferOverwrite, progress.report)
			if err != nil {
				return err
			}
			transferConfig := service.ConfigManager().Config()
			if transferConfig.Notifications.Enabled {
				_ = (desktopnotify.SystemNotifier{}).Notify("文件传输完成", transferLocal+" → "+transferRemote)
			}
			_, err = fmt.Fprintln(os.Stdout, "文件传输完成。")
			return err
		}
		if (exportPath != "" || exportXShellPath != "" || exportXFTPPath != "") && importPath != "" {
			return errors.New("会话导出和导入不能在同一次运行中使用")
		}
		if (exportPath != "" && exportXShellPath != "") || (exportPath != "" && exportXFTPPath != "") || (exportXShellPath != "" && exportXFTPPath != "") {
			return errors.New("一次只能使用 --export-sessions、--export-xshell 或 --export-xftp 其中一个")
		}
		if exportXFTPPath != "" {
			return runXFTPExportCommand(ctx, databasePath, exportXFTPPath)
		}
		if exportXShellPath != "" {
			return runXShellExportCommand(ctx, databasePath, exportXShellPath)
		}
		if exportPath != "" || importPath != "" {
			return runSessionCatalogCommand(ctx, databasePath, exportPath, importPath)
		}

		application, err := app.NewWithDatabaseAndConfig(ctx, databasePath, service.ConfigManager().Config())
		if err != nil {
			return err
		}
		return application.Run()
	},
}

func runXShellExportCommand(ctx context.Context, databasePath, exportPath string) (runErr error) {
	storage, err := sqlite.Open(ctx, databasePath)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := storage.Close(); runErr == nil && closeErr != nil {
			runErr = fmt.Errorf("close local database: %w", closeErr)
		}
	}()
	groups, err := storage.ListGroups(ctx)
	if err != nil {
		return err
	}
	hosts, err := storage.ListHosts(ctx)
	if err != nil {
		return err
	}
	rules := make([]model.PortForwardRule, 0)
	for _, host := range hosts {
		if host.Kind != model.HostKindSSH {
			continue
		}
		hostRules, err := storage.ListPortForwardRules(ctx, host.Id)
		if err != nil {
			return err
		}
		rules = append(rules, hostRules...)
	}
	result, err := sessionfile.WriteXShellDirectory(exportPath, groups, hosts, rules)
	if err != nil {
		return err
	}
	if _, err = fmt.Fprintf(os.Stdout, "已导出 %d 个 Xshell 会话到 %s（POSIX 目录权限 0700、文件权限 0600；不包含密码或本机凭据引用）\n",
		result.Exported, exportPath); err != nil {
		return err
	}
	for _, reason := range result.Skipped {
		if _, err = fmt.Fprintf(os.Stdout, "已跳过：%s\n", reason); err != nil {
			return err
		}
	}
	return nil
}

func runXFTPExportCommand(ctx context.Context, databasePath, exportPath string) (runErr error) {
	storage, err := sqlite.Open(ctx, databasePath)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := storage.Close(); runErr == nil && closeErr != nil {
			runErr = fmt.Errorf("close local database: %w", closeErr)
		}
	}()
	groups, err := storage.ListGroups(ctx)
	if err != nil {
		return err
	}
	hosts, err := storage.ListHosts(ctx)
	if err != nil {
		return err
	}
	result, err := sessionfile.WriteXFTPDirectory(exportPath, groups, hosts)
	if err != nil {
		return err
	}
	if _, err = fmt.Fprintf(os.Stdout, "已导出 %d 个 Xftp 会话到 %s（POSIX 目录权限 0700、文件权限 0600；不包含密码或本机凭据引用）\n",
		result.Exported, exportPath); err != nil {
		return err
	}
	for _, reason := range result.Skipped {
		if _, err = fmt.Fprintf(os.Stdout, "已跳过：%s\n", reason); err != nil {
			return err
		}
	}
	return nil
}

type cliTransferProgress struct {
	mu       sync.Mutex
	last     time.Time
	lastByte int64
}

func (progress *cliTransferProgress) report(current, total int64) {
	progress.mu.Lock()
	defer progress.mu.Unlock()
	now := time.Now()
	if current < total && now.Sub(progress.last) < 250*time.Millisecond && current-progress.lastByte < 1<<20 {
		return
	}
	if total > 0 {
		_, _ = fmt.Fprintf(os.Stdout, "传输进度：%d/%d 字节（%.1f%%）\n", current, total, float64(current)*100/float64(total))
	} else {
		_, _ = fmt.Fprintf(os.Stdout, "传输进度：%d 字节\n", current)
	}
	progress.last, progress.lastByte = now, current
}

func printScheduledTransferEvent(event scheduledtransfer.Event) {
	switch event.Phase {
	case "started":
		_, _ = fmt.Fprintf(os.Stdout, "计划传输开始：%s\n", event.Transfer.Name)
	case "finished":
		if event.Status == model.ScheduledTransferSucceeded {
			_, _ = fmt.Fprintf(os.Stdout, "计划传输完成：%s\n", event.Transfer.Name)
			return
		}
		_, _ = fmt.Fprintf(os.Stdout, "计划传输%s：%s", scheduledTransferStatusText(event.Status), event.Transfer.Name)
		if event.Err != nil {
			_, _ = fmt.Fprintf(os.Stdout, "：%v", event.Err)
		}
		_, _ = fmt.Fprintln(os.Stdout)
	}
}

func scheduledTransferStatusText(status model.ScheduledTransferStatus) string {
	switch status {
	case model.ScheduledTransferCanceled:
		return "已取消"
	case model.ScheduledTransferFailed:
		return "失败"
	default:
		return string(status)
	}
}

func runSessionCatalogCommand(ctx context.Context, databasePath, exportPath, importPath string) (runErr error) {
	var imported sessionfile.Catalog
	if importPath != "" {
		var err error
		imported, err = sessionfile.ReadFile(importPath)
		if err != nil {
			return err
		}
	}

	storage, err := sqlite.Open(ctx, databasePath)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := storage.Close(); runErr == nil && closeErr != nil {
			runErr = fmt.Errorf("close local database: %w", closeErr)
		}
	}()

	if exportPath != "" {
		groups, err := storage.ListGroups(ctx)
		if err != nil {
			return err
		}
		hosts, err := storage.ListHosts(ctx)
		if err != nil {
			return err
		}
		rules := make([]model.PortForwardRule, 0)
		for _, host := range hosts {
			if host.Kind != model.HostKindSSH {
				continue
			}
			hostRules, err := storage.ListPortForwardRules(ctx, host.Id)
			if err != nil {
				return err
			}
			rules = append(rules, hostRules...)
		}
		catalog := sessionfile.NewCatalog(groups, hosts, rules)
		if err := sessionfile.WriteFile(exportPath, catalog); err != nil {
			return err
		}
		_, err = fmt.Fprintf(os.Stdout, "已导出 %d 个分组、%d 个主机和 %d 条转发规则到 %s（POSIX 文件权限 0600；不包含密码、本机凭据引用或启动脚本）\n",
			len(groups), len(hosts), len(rules), exportPath)
		return err
	}

	result, err := storage.ImportCatalog(ctx, imported.Groups, imported.HostRecords(), imported.PortForwardRules)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(os.Stdout, "已导入 %d 个分组、%d 个主机和 %d 条转发规则；密码、本机凭据引用和启动脚本未导入\n",
		result.Groups, result.Hosts, result.PortForwardRules)
	return err
}
