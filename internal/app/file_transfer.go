package app

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"tui-ssh-workbench/internal/infra/credentialstore"
	"tui-ssh-workbench/internal/infra/sqlite"
	"tui-ssh-workbench/internal/infra/sshclient"
	"tui-ssh-workbench/internal/model"
)

// RunFileTransfer transfers one saved host's local file or directory without
// starting the TUI. The host selector accepts an ID or an unambiguous name.
func RunFileTransfer(ctx context.Context, databasePath string, config model.Config, hostSelector string, direction model.ScheduledTransferDirection, localPath, remotePath string, overwrite bool, progress func(current, total int64)) (runErr error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if direction != model.ScheduledTransferUpload && direction != model.ScheduledTransferDownload {
		return errors.New("传输方向必须是 upload 或 download")
	}
	if strings.TrimSpace(hostSelector) == "" {
		return errors.New("必须指定主机 ID 或唯一名称")
	}
	if strings.TrimSpace(localPath) == "" || strings.TrimSpace(remotePath) == "" {
		return errors.New("本地路径和远程路径不能为空")
	}
	absoluteLocalPath, err := filepath.Abs(localPath)
	if err != nil {
		return fmt.Errorf("解析本地路径失败：%w", err)
	}
	config = config.WithDefaults()
	if err := config.Validate(); err != nil {
		return err
	}
	storage, err := sqlite.Open(ctx, databasePath)
	if err != nil {
		return fmt.Errorf("打开主机目录失败：%w", err)
	}
	defer func() {
		if err := storage.Close(); runErr == nil && err != nil {
			runErr = fmt.Errorf("关闭主机目录失败：%w", err)
		}
	}()

	host, err := findTransferHost(ctx, storage, hostSelector)
	if err != nil {
		return err
	}
	if host.Kind != model.HostKindSSH && host.Kind != model.HostKindFTP {
		return fmt.Errorf("主机 %q 的类型 %q 不支持文件传输", host.Name, host.Kind)
	}
	options := sshclient.OptionsFromSSHConfig(config.SSH)
	executor := scheduledTransferExecutor{
		storage: storage, vault: credentialstore.New(), sshOptions: options, progress: progress,
	}
	return executor.Execute(ctx, model.ScheduledTransfer{
		Name: host.Name, HostId: host.Id, Direction: direction,
		LocalPath: absoluteLocalPath, RemotePath: remotePath, Overwrite: overwrite,
	})
}

func findTransferHost(ctx context.Context, storage *sqlite.Store, selector string) (model.Host, error) {
	selector = strings.TrimSpace(selector)
	hosts, err := storage.ListHosts(ctx)
	if err != nil {
		return model.Host{}, fmt.Errorf("读取主机目录失败：%w", err)
	}
	for _, host := range hosts {
		if host.Id == selector {
			return host, nil
		}
	}
	var matches []model.Host
	for _, host := range hosts {
		if strings.EqualFold(host.Name, selector) {
			matches = append(matches, host)
		}
	}
	switch len(matches) {
	case 0:
		return model.Host{}, fmt.Errorf("找不到主机 ID 或名称 %q", selector)
	case 1:
		return matches[0], nil
	default:
		ids := make([]string, len(matches))
		for index, host := range matches {
			ids[index] = host.Id
		}
		return model.Host{}, fmt.Errorf("主机名称 %q 不唯一，请使用主机 ID（%s）", selector, strings.Join(ids, ", "))
	}
}
