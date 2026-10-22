package cmd

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"tui-ssh-workbench/internal/infra/sessionfile"
	"tui-ssh-workbench/internal/infra/sqlite"
	_ "tui-ssh-workbench/internal/logic"
	"tui-ssh-workbench/internal/model"
)

func TestSessionCatalogCommandExportsAndImports(t *testing.T) {
	ctx := context.Background()
	sourceDB := filepath.Join(t.TempDir(), "source.db")
	source, err := sqlite.Open(ctx, sourceDB)
	if err != nil {
		t.Fatalf("open source database: %v", err)
	}
	group, err := source.CreateGroup(ctx, model.Group{Name: "Operations"})
	if err != nil {
		t.Fatalf("create group: %v", err)
	}
	host, err := source.CreateHost(ctx, model.Host{
		Kind: model.HostKindSSH, Name: "gateway", IP: "192.0.2.10", Port: 2222,
		User: "operator", GroupId: group.Id, Password: "not-persisted",
	})
	if err != nil {
		t.Fatalf("create host: %v", err)
	}
	if _, err := source.SavePortForwardRule(ctx, model.PortForwardRule{
		HostId: host.Id, Kind: model.PortForwardSOCKS5, ListenAddress: "127.0.0.1:1080",
	}); err != nil {
		t.Fatalf("save forwarding rule: %v", err)
	}
	if err := source.Close(); err != nil {
		t.Fatalf("close source database: %v", err)
	}

	filePath := filepath.Join(t.TempDir(), "sessions.json")
	if err := runSessionCatalogCommand(ctx, sourceDB, filePath, ""); err != nil {
		t.Fatalf("export sessions: %v", err)
	}
	catalog, err := sessionfile.ReadFile(filePath)
	if err != nil {
		t.Fatalf("read exported catalog: %v", err)
	}
	if len(catalog.Groups) != 1 || len(catalog.Hosts) != 1 || len(catalog.PortForwardRules) != 1 {
		t.Fatalf("exported catalog = %#v", catalog)
	}

	destinationDB := filepath.Join(t.TempDir(), "destination.db")
	if err := runSessionCatalogCommand(ctx, destinationDB, "", filePath); err != nil {
		t.Fatalf("import sessions: %v", err)
	}
	destination, err := sqlite.Open(ctx, destinationDB)
	if err != nil {
		t.Fatalf("open imported database: %v", err)
	}
	defer func() { _ = destination.Close() }()
	importedHost, err := destination.GetHost(ctx, host.Id)
	if err != nil || importedHost.Name != host.Name || importedHost.Password != "" || importedHost.CredentialRef != "" {
		t.Fatalf("imported host = %#v, %v", importedHost, err)
	}
	rules, err := destination.ListPortForwardRules(ctx, host.Id)
	if err != nil || len(rules) != 1 {
		t.Fatalf("imported forwarding rules = %#v, %v", rules, err)
	}
}

func TestSessionCatalogCommandImportsXShellSession(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "edge.xsh")
	contents := `[SessionInfo]
Version=7.0
SessionName=production-edge
[CONNECTION]
Protocol=SSH
Host=192.0.2.12
Port=2222
[CONNECTION:AUTHENTICATION]
UserName=operator
Password=ignored-secret
`
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	databasePath := filepath.Join(t.TempDir(), "imported.db")
	if err := runSessionCatalogCommand(ctx, databasePath, "", path); err != nil {
		t.Fatalf("import Xshell session: %v", err)
	}
	store, err := sqlite.Open(ctx, databasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	hosts, err := store.ListHosts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(hosts) != 1 || hosts[0].Name != "production-edge" || hosts[0].IP != "192.0.2.12" ||
		hosts[0].Port != 2222 || hosts[0].User != "operator" || hosts[0].Password != "" || hosts[0].CredentialRef != "" {
		t.Fatalf("imported Xshell hosts = %#v", hosts)
	}
}

func TestXShellDirectoryCommandExportsAndImportsSessions(t *testing.T) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "source.db")
	storage, err := sqlite.Open(ctx, databasePath)
	if err != nil {
		t.Fatal(err)
	}
	group, err := storage.CreateGroup(ctx, model.Group{Name: "Operations"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := storage.CreateHost(ctx, model.Host{
		Kind: model.HostKindSSH, Name: "edge", IP: "192.0.2.10", Port: 2222,
		User: "operator", GroupId: group.Id,
	}); err != nil {
		t.Fatal(err)
	}
	if err := storage.Close(); err != nil {
		t.Fatal(err)
	}

	configPath := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(configPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	exportPath := filepath.Join(t.TempDir(), "xshell")
	if _, err := Main.RunWithSpecificArgs(ctx, []string{
		"tui-ssh-workbench", "--config", configPath, "--db", databasePath,
		"--export-xshell", exportPath,
	}); err != nil {
		t.Fatalf("run native Xshell export: %v", err)
	}
	if _, err := os.Stat(filepath.Join(exportPath, "Operations", "edge.xsh")); err != nil {
		t.Fatalf("exported native session missing: %v", err)
	}

	importDB := filepath.Join(t.TempDir(), "import.db")
	if _, err := Main.RunWithSpecificArgs(ctx, []string{
		"tui-ssh-workbench", "--config", configPath, "--db", importDB,
		"--import-sessions", exportPath,
	}); err != nil {
		t.Fatalf("run Xshell directory import: %v", err)
	}
	imported, err := sqlite.Open(ctx, importDB)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = imported.Close() }()
	hosts, err := imported.ListHosts(ctx)
	if err != nil || len(hosts) != 1 || hosts[0].Name != "edge" || hosts[0].GroupId == "" {
		t.Fatalf("imported native sessions = %#v, %v", hosts, err)
	}
	groups, err := imported.ListGroups(ctx)
	if err != nil || len(groups) != 1 || groups[0].Name != "Operations" {
		t.Fatalf("imported native groups = %#v, %v", groups, err)
	}
}

func TestXFTPExportCommandWritesNativeSessions(t *testing.T) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "source.db")
	storage, err := sqlite.Open(ctx, databasePath)
	if err != nil {
		t.Fatal(err)
	}
	group, err := storage.CreateGroup(ctx, model.Group{Name: "Files"})
	if err != nil {
		t.Fatal(err)
	}
	for _, host := range []model.Host{
		{Kind: model.HostKindSSH, Name: "sftp", IP: "sftp.example.test", Port: 22, GroupId: group.Id},
		{Kind: model.HostKindFTP, Name: "ftp", IP: "ftp.example.test", Port: 21, TLSMode: model.FTPTLSOff, GroupId: group.Id},
	} {
		if _, err := storage.CreateHost(ctx, host); err != nil {
			t.Fatal(err)
		}
	}
	if err := storage.Close(); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(configPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	exportPath := filepath.Join(t.TempDir(), "xftp-sessions")
	if _, err := Main.RunWithSpecificArgs(ctx, []string{
		"tui-ssh-workbench", "--config", configPath, "--db", databasePath,
		"--export-xftp", exportPath,
	}); err != nil {
		t.Fatalf("run Xftp export: %v", err)
	}
	for _, filename := range []string{"sftp.xfp", "ftp.xfp"} {
		if _, err := os.Stat(filepath.Join(exportPath, "Files", filename)); err != nil {
			t.Errorf("native Xftp session %q missing: %v", filename, err)
		}
	}
}

func TestMainCommandAcceptsSessionExportFlags(t *testing.T) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "workbench.db")
	configPath := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(configPath, nil, 0o600); err != nil {
		t.Fatalf("create config fixture: %v", err)
	}
	exportPath := filepath.Join(t.TempDir(), "sessions.json")
	_, err := Main.RunWithSpecificArgs(ctx, []string{
		"tui-ssh-workbench", "--config", configPath, "--db", databasePath,
		"--export-sessions", exportPath,
	})
	if err != nil {
		t.Fatalf("run export command: %v", err)
	}
	if _, err := sessionfile.ReadFile(exportPath); err != nil {
		t.Fatalf("export flag did not produce a valid catalog: %v", err)
	}
	importDB := filepath.Join(t.TempDir(), "imported.db")
	_, err = Main.RunWithSpecificArgs(ctx, []string{
		"tui-ssh-workbench", "--config", configPath, "--db", importDB,
		"--import-sessions", exportPath,
	})
	if err != nil {
		t.Fatalf("run import command: %v", err)
	}
	imported, err := sqlite.Open(ctx, importDB)
	if err != nil {
		t.Fatalf("open imported database: %v", err)
	}
	defer func() { _ = imported.Close() }()
	if hosts, err := imported.ListHosts(ctx); err != nil || len(hosts) != 0 {
		t.Fatalf("import empty catalog hosts = %v, %v; want empty", hosts, err)
	}
}

func TestMainCommandRunsScheduledTransferServiceUntilCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	configPath := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(configPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	databasePath := filepath.Join(t.TempDir(), "scheduled.db")
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()
	_, err := Main.RunWithSpecificArgs(ctx, []string{
		"tui-ssh-workbench", "--config", configPath, "--db", databasePath, "--run-scheduled",
	})
	if err != nil {
		t.Fatalf("run scheduled transfer service flag: %v", err)
	}
	if _, err := os.Stat(databasePath); err != nil {
		t.Fatalf("scheduled transfer command did not initialize its database: %v", err)
	}
}

func TestMainCommandSelectsOneShotTransferMode(t *testing.T) {
	ctx := context.Background()
	configPath := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(configPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	databasePath := filepath.Join(t.TempDir(), "transfer.db")
	args := []string{
		"tui-ssh-workbench", "--config", configPath, "--db", databasePath,
		"--transfer-host", "missing", "--transfer-direction", "upload",
		"--transfer-local", "/tmp/source", "--transfer-remote", "/tmp/target",
	}
	if _, err := Main.RunWithSpecificArgs(ctx, args); err == nil || !strings.Contains(err.Error(), "必须与 --transfer") {
		t.Fatalf("transfer parameters without --transfer error = %v", err)
	}
	args = []string{
		"tui-ssh-workbench", "--config", configPath, "--db", databasePath,
		"--transfer", "--transfer-host", "missing", "--transfer-direction", "upload",
		"--transfer-local", "/tmp/source", "--transfer-remote", "/tmp/target",
	}
	if _, err := Main.RunWithSpecificArgs(ctx, args); err == nil || !strings.Contains(err.Error(), "找不到主机") {
		t.Fatalf("one-shot transfer mode error = %v; want host lookup error", err)
	}
}
