package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"tui-ssh-workbench/internal/infra/credentialstore"
	"tui-ssh-workbench/internal/infra/desktopnotify"
	"tui-ssh-workbench/internal/infra/ftpclient"
	"tui-ssh-workbench/internal/infra/remotefs"
	"tui-ssh-workbench/internal/infra/scpclient"
	"tui-ssh-workbench/internal/infra/sftpclient"
	"tui-ssh-workbench/internal/infra/sqlite"
	"tui-ssh-workbench/internal/infra/sshclient"
	"tui-ssh-workbench/internal/logic/scheduledtransfer"
	"tui-ssh-workbench/internal/model"
)

// RunScheduledTransfers starts the foreground scheduler used by the CLI mode.
// Each plan runs independently of the TUI, but still uses the same SQLite
// catalog, host-key policy and system-keyring credentials.
func RunScheduledTransfers(ctx context.Context, databasePath string, config model.Config, onEvent func(scheduledtransfer.Event)) (runErr error) {
	return runScheduledTransfersWithFTP(ctx, databasePath, config, nil, onEvent)
}

func runScheduledTransfersWithFTP(ctx context.Context, databasePath string, config model.Config, ftpDial func(context.Context, ftpclient.Config) (*ftpclient.Client, error), onEvent func(scheduledtransfer.Event)) (runErr error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if ctx.Err() != nil {
		return nil
	}
	config = config.WithDefaults()
	if err := config.Validate(); err != nil {
		return err
	}
	storage, err := sqlite.Open(ctx, databasePath)
	if err != nil {
		if ctx.Err() != nil && errors.Is(err, ctx.Err()) {
			return nil
		}
		return fmt.Errorf("open scheduled transfer database: %w", err)
	}
	defer func() {
		if err := storage.Close(); runErr == nil && err != nil {
			runErr = fmt.Errorf("close scheduled transfer database: %w", err)
		}
	}()
	return runScheduledTransfersWithStore(ctx, storage, config, ftpDial, onEvent)
}

// runScheduledTransfersWithStore runs a scheduler against an already-open
// repository. The workbench uses this to share its SQLite store with the UI
// and avoid opening a second, isolated :memory: database.
func runScheduledTransfersWithStore(ctx context.Context, storage *sqlite.Store, config model.Config, ftpDial func(context.Context, ftpclient.Config) (*ftpclient.Client, error), onEvent func(scheduledtransfer.Event)) error {
	if storage == nil {
		return errors.New("scheduled transfer database is not configured")
	}
	options := sshclient.OptionsFromSSHConfig(config.SSH)
	executor := scheduledTransferExecutor{storage: storage, vault: credentialstore.New(), sshOptions: options, ftpDial: ftpDial}
	runner := scheduledtransfer.Runner{
		Repository: storage,
		Executor:   executor,
		OnEvent:    scheduledTransferEventHandler(config.Notifications.Enabled, desktopnotify.SystemNotifier{}, onEvent),
	}
	return runner.Run(ctx)
}

func scheduledTransferEventHandler(enabled bool, notifier desktopnotify.Notifier, next func(scheduledtransfer.Event)) func(scheduledtransfer.Event) {
	return func(event scheduledtransfer.Event) {
		if event.Phase == "finished" && enabled && notifier != nil {
			title := "计划传输完成"
			if event.Status != model.ScheduledTransferSucceeded {
				title = "计划传输未完成"
			}
			_ = notifier.Notify(title, "计划任务："+event.Transfer.Name)
		}
		if next != nil {
			next(event)
		}
	}
}

type scheduledTransferExecutor struct {
	storage    *sqlite.Store
	vault      *credentialstore.Store
	sshOptions sshclient.ConnectOptions
	ftpDial    func(context.Context, ftpclient.Config) (*ftpclient.Client, error)
	progress   func(current, total int64)
}

func (executor scheduledTransferExecutor) Execute(ctx context.Context, transfer model.ScheduledTransfer) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !filepath.IsAbs(transfer.LocalPath) {
		return errors.New("计划任务的本地路径必须是绝对路径")
	}
	host, err := executor.storage.GetHost(ctx, transfer.HostId)
	if err != nil {
		return fmt.Errorf("读取计划任务主机失败：%w", err)
	}
	var uploadInfo os.FileInfo
	if transfer.Direction == model.ScheduledTransferUpload {
		uploadInfo, err = os.Lstat(transfer.LocalPath)
		if err != nil {
			return fmt.Errorf("读取本地源路径失败：%w", err)
		}
		if uploadInfo.Mode()&os.ModeSymlink != 0 {
			return errors.New("文件传输不跟随本地符号链接")
		}
		if !uploadInfo.IsDir() && !uploadInfo.Mode().IsRegular() {
			return errors.New("文件传输只支持普通文件或目录")
		}
	}
	if host.Kind == model.HostKindSSH && host.FileProtocol == model.SSHFileProtocolSCP {
		return executor.executeSCP(ctx, host, transfer, uploadInfo)
	}
	var client remotefs.Client
	switch host.Kind {
	case model.HostKindSSH:
		client, err = executor.openSFTP(ctx, host)
	case model.HostKindFTP:
		client, err = executor.openFTP(ctx, host)
	default:
		err = fmt.Errorf("主机类型 %q 不支持计划传输", host.Kind)
	}
	if err != nil {
		return err
	}
	defer client.Close()

	switch transfer.Direction {
	case model.ScheduledTransferUpload:
		if uploadInfo.IsDir() {
			return client.UploadDirectory(ctx, transfer.LocalPath, transfer.RemotePath, transfer.Overwrite, executor.progress)
		}
		return client.Upload(ctx, transfer.LocalPath, transfer.RemotePath, transfer.Overwrite, executor.progress)
	case model.ScheduledTransferDownload:
		entry, err := scheduledRemoteEntry(client, transfer.RemotePath)
		if err != nil {
			return err
		}
		if entry.IsSymlink {
			return errors.New("计划任务不跟随远程符号链接")
		}
		if entry.IsDir {
			return client.DownloadDirectory(ctx, transfer.RemotePath, transfer.LocalPath, transfer.Overwrite, executor.progress)
		}
		return client.Download(ctx, transfer.RemotePath, transfer.LocalPath, transfer.Overwrite, executor.progress)
	default:
		return fmt.Errorf("未知计划传输方向：%q", transfer.Direction)
	}
}

func (executor scheduledTransferExecutor) openSFTP(ctx context.Context, host model.Host) (remotefs.Client, error) {
	session, err := executor.openSSHSession(ctx, host)
	if err != nil {
		return nil, err
	}
	client, err := session.OpenSFTP()
	if err != nil {
		_ = session.Close()
		return nil, fmt.Errorf("打开 SFTP 子系统失败：%w", err)
	}
	return &sessionSFTPClient{Client: client, session: session}, nil
}

func (executor scheduledTransferExecutor) executeSCP(ctx context.Context, host model.Host, transfer model.ScheduledTransfer, uploadInfo os.FileInfo) error {
	session, err := executor.openSSHSession(ctx, host)
	if err != nil {
		return err
	}
	defer session.Close()
	client, err := scpclient.New(func() (scpclient.Command, error) {
		return session.OpenCommand()
	})
	if err != nil {
		return err
	}
	switch transfer.Direction {
	case model.ScheduledTransferUpload:
		if uploadInfo.IsDir() {
			return client.UploadDirectory(ctx, transfer.LocalPath, transfer.RemotePath, transfer.Overwrite, executor.progress)
		}
		return client.Upload(ctx, transfer.LocalPath, transfer.RemotePath, transfer.Overwrite, executor.progress)
	case model.ScheduledTransferDownload:
		return client.Download(ctx, transfer.RemotePath, transfer.LocalPath, transfer.Overwrite, executor.progress)
	default:
		return fmt.Errorf("未知 SCP 传输方向：%q", transfer.Direction)
	}
}

func (executor scheduledTransferExecutor) openSSHSession(ctx context.Context, host model.Host) (*sshclient.Session, error) {
	hosts, err := executor.storage.ListHosts(ctx)
	if err != nil {
		return nil, fmt.Errorf("读取 SSH 主机目录失败：%w", err)
	}
	profiles := make(map[string]model.AuthProfile)
	resolveProfile := func(saved model.Host) (model.Host, error) {
		if saved.Kind != model.HostKindSSH {
			return saved, nil
		}
		profileID, err := executor.storage.EffectiveAuthProfileID(ctx, saved)
		if err != nil {
			return model.Host{}, fmt.Errorf("读取主机 %q 的分组认证配置失败：%w", saved.Name, err)
		}
		if profileID == "" {
			return saved, nil
		}
		profile, ok := profiles[profileID]
		if !ok {
			profile, err = executor.storage.GetAuthProfile(ctx, profileID)
			if err != nil {
				return model.Host{}, fmt.Errorf("读取主机 %q 的认证配置档失败：%w", saved.Name, err)
			}
			profiles[profileID] = profile
		}
		return saved.WithAuthProfile(profile), nil
	}
	hostFound := false
	for index := range hosts {
		resolved, err := resolveProfile(hosts[index])
		if err != nil {
			return nil, err
		}
		hosts[index] = resolved
		if hosts[index].Id == host.Id {
			host = resolved
			hostFound = true
		}
	}
	if !hostFound {
		// The target may not have appeared in ListHosts (for example, a custom
		// storage implementation). Resolve it independently in that case.
		resolved, err := resolveProfile(host)
		if err != nil {
			return nil, err
		}
		host = resolved
	}
	hostByID := make(map[string]model.Host, len(hosts))
	for _, savedHost := range hosts {
		hostByID[savedHost.Id] = savedHost
	}
	credentials := make(map[string]string, len(host.ProxyJumpList)+1)
	defer clearCredentialMap(credentials)
	credentialHosts := make([]model.Host, 0, len(host.ProxyJumpList)+1)
	for _, jumpID := range host.ProxyJumpList {
		jump, ok := hostByID[jumpID]
		if !ok {
			return nil, fmt.Errorf("计划任务引用的跳板机 %q 不存在", jumpID)
		}
		credentialHosts = append(credentialHosts, jump)
	}
	credentialHosts = append(credentialHosts, host)
	for _, credentialHost := range credentialHosts {
		if credentialHost.CredentialRef == "" {
			continue
		}
		secret, err := executor.vault.Get(credentialHost.CredentialRef)
		if err != nil {
			return nil, fmt.Errorf("读取主机 %q 的系统钥匙串凭据失败：%w", credentialHost.Name, err)
		}
		credentials[sshclient.HostCredentialKey(credentialHost)] = secret
	}
	session, err := sshclient.ConnectTransportWithJumpsAndCredentialsWithOptions(
		ctx, host, credentials, hosts, nil, executor.sshOptions,
	)
	if err != nil {
		return nil, fmt.Errorf("连接 SSH 主机 %q 失败：%w", host.Name, err)
	}
	return session, nil
}

func (executor scheduledTransferExecutor) openFTP(ctx context.Context, host model.Host) (*ftpclient.Client, error) {
	password := ""
	if host.CredentialRef != "" {
		secret, err := executor.vault.Get(host.CredentialRef)
		if err != nil {
			return nil, fmt.Errorf("读取 FTP 主机 %q 的系统钥匙串凭据失败：%w", host.Name, err)
		}
		password = secret
	} else if host.User != "" && !strings.EqualFold(host.User, "anonymous") {
		return nil, errors.New("FTP/FTPS 计划任务需要先将密码保存到系统钥匙串")
	}
	dial := executor.ftpDial
	if dial == nil {
		dial = ftpclient.Dial
	}
	client, err := dial(ctx, ftpclient.Config{
		Address: host.IP, Port: host.Port, Username: host.User,
		Password: password, TLSMode: ftpclient.TLSMode(host.TLSMode), ActiveMode: host.FTPActiveMode,
	})
	password = ""
	if err != nil {
		return nil, fmt.Errorf("连接 FTP/FTPS 主机 %q 失败：%w", host.Name, err)
	}
	return client, nil
}

// sessionSFTPClient couples the SFTP subsystem and its SSH transport so a
// scheduled transfer cannot leave an authenticated background connection open.
type sessionSFTPClient struct {
	*sftpclient.Client
	session *sshclient.Session
}

func (client *sessionSFTPClient) Close() error {
	sftpErr := client.Client.Close()
	sshErr := client.session.Close()
	return errors.Join(sftpErr, sshErr)
}

func clearCredentialMap(credentials map[string]string) {
	for key := range credentials {
		credentials[key] = ""
		delete(credentials, key)
	}
}

func scheduledRemoteEntry(client remotefs.Client, remotePath string) (remotefs.Entry, error) {
	remotePath = strings.TrimSpace(remotePath)
	if remotePath == "" {
		return remotefs.Entry{}, errors.New("远程路径不能为空")
	}
	cleaned := path.Clean(remotePath)
	if cleaned == "." || cleaned == "/" {
		return remotefs.Entry{Name: path.Base(cleaned), IsDir: true}, nil
	}
	parent, name := path.Split(cleaned)
	parent = strings.TrimSuffix(parent, "/")
	if parent == "" {
		parent = "."
	}
	entries, err := client.List(parent)
	if err != nil {
		return remotefs.Entry{}, fmt.Errorf("读取远程路径所在目录失败：%w", err)
	}
	for _, entry := range entries {
		if entry.Name == name {
			return entry, nil
		}
	}
	return remotefs.Entry{}, fmt.Errorf("远程路径 %q 不存在或不可见", remotePath)
}
