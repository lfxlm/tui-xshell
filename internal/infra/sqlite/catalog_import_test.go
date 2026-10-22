package sqlite

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"tui-ssh-workbench/internal/model"
)

func TestImportCatalogIsAtomicAndIdempotent(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "workbench.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	groups := []model.Group{
		{Id: "group-child", Name: "Application", ParentId: "group-root", LocalStartDir: "~/local-app", RemoteStartDir: "/srv/apps"},
		{Id: "group-root", Name: "Production", LocalStartDir: "~/local-root", RemoteStartDir: "/srv"},
	}
	hosts := []model.Host{
		{Id: "app-host", Kind: model.HostKindSSH, FileProtocol: model.SSHFileProtocolSCP, X11Forwarding: true, Name: "app", IP: "192.0.2.11", Port: 22, User: "deploy", GroupId: "group-child", Favorite: true, ProxyJumpList: []string{"gateway"}, TerminalEncoding: "GB18030", TerminalInputEncoding: model.TerminalEncodingUTF8},
		{Id: "gateway", Kind: model.HostKindSSH, Name: "gateway", IP: "192.0.2.10", Port: 2222, User: "ops", GroupId: "group-root"},
		{Id: "ftp-host", Kind: model.HostKindFTP, Name: "archive", IP: "files.example.test", TLSMode: model.FTPTLSExplicit, FTPActiveMode: true},
		{Id: "local-host", Kind: model.HostKindLocal, Name: "local"},
		{Id: "telnet-host", Kind: model.HostKindTelnet, Name: "console", IP: "192.0.2.44", User: "operator"},
		{Id: "rlogin-host", Kind: model.HostKindTelnet, TerminalProtocol: model.TerminalProtocolRlogin, Name: "legacy", IP: "192.0.2.45", User: "operator"},
		{Id: "serial-host", Kind: model.HostKindSerial, Name: "console-port", IP: "/dev/ttyUSB1", SerialBaudRate: 57600, SerialDataBits: 8, SerialParity: model.SerialParityOdd, SerialStopBits: model.SerialStopBitsOne5, SerialFlowControl: model.SerialFlowControlXONXOFF},
	}
	rules := []model.PortForwardRule{{
		Id: "forward-rule", HostId: "gateway", Kind: model.PortForwardSOCKS5,
		ListenAddress: "127.0.0.1:1080",
	}}

	result, err := store.ImportCatalog(ctx, groups, hosts, rules)
	if err != nil {
		t.Fatalf("import catalog: %v", err)
	}
	if result != (CatalogImportResult{Groups: 2, Hosts: 7, PortForwardRules: 1}) {
		t.Fatalf("import result = %#v", result)
	}
	importedGroup, err := store.GetGroup(ctx, "group-child")
	if err != nil || importedGroup.LocalStartDir != "" || importedGroup.RemoteStartDir != "/srv/apps" {
		t.Fatalf("imported group start paths = %#v, %v", importedGroup, err)
	}

	app, err := store.GetHost(ctx, "app-host")
	if err != nil || len(app.ProxyJumpList) != 1 || app.ProxyJumpList[0] != "gateway" || !app.Favorite || app.FileProtocol != model.SSHFileProtocolSCP || !app.X11Forwarding || app.TerminalEncoding != "GB18030" || app.TerminalInputEncoding != model.TerminalEncodingUTF8 {
		t.Fatalf("imported jump host = %#v, %v", app, err)
	}
	ftpHost, err := store.GetHost(ctx, "ftp-host")
	if err != nil || ftpHost.Kind != model.HostKindFTP || ftpHost.TLSMode != model.FTPTLSExplicit || ftpHost.Port != 21 || !ftpHost.FTPActiveMode {
		t.Fatalf("imported FTP host = %#v, %v", ftpHost, err)
	}
	telnetHost, err := store.GetHost(ctx, "telnet-host")
	if err != nil || telnetHost.Kind != model.HostKindTelnet || telnetHost.Port != 23 || telnetHost.User != "operator" {
		t.Fatalf("imported Telnet host = %#v, %v", telnetHost, err)
	}
	rloginHost, err := store.GetHost(ctx, "rlogin-host")
	if err != nil || rloginHost.Kind != model.HostKindTelnet || rloginHost.TerminalProtocol != model.TerminalProtocolRlogin || rloginHost.Port != 513 {
		t.Fatalf("imported Rlogin host = %#v, %v", rloginHost, err)
	}
	serialHost, err := store.GetHost(ctx, "serial-host")
	if err != nil || serialHost.Kind != model.HostKindSerial || serialHost.IP != "/dev/ttyUSB1" ||
		serialHost.SerialBaudRate != 57600 || serialHost.SerialDataBits != 8 || serialHost.SerialParity != model.SerialParityOdd ||
		serialHost.SerialStopBits != model.SerialStopBitsOne5 || serialHost.SerialFlowControl != model.SerialFlowControlXONXOFF {
		t.Fatalf("imported serial host = %#v, %v", serialHost, err)
	}

	gateway, err := store.GetHost(ctx, "gateway")
	if err != nil {
		t.Fatal(err)
	}
	gateway.CredentialRef = "local-keychain-entry"
	if err := store.UpdateHost(ctx, gateway); err != nil {
		t.Fatalf("set local credential reference: %v", err)
	}
	if err := store.RecordHostConnection(ctx, gateway.Id, time.Now().UTC()); err != nil {
		t.Fatalf("set local recent connection metadata: %v", err)
	}
	localProfile, err := store.CreateAuthProfile(ctx, model.AuthProfile{Name: "Local Engineering"})
	if err != nil {
		t.Fatalf("create local inherited profile: %v", err)
	}
	rootGroup, err := store.GetGroup(ctx, "group-root")
	if err != nil {
		t.Fatalf("read root group: %v", err)
	}
	rootGroup.AuthProfileID = localProfile.Id
	if err := store.UpdateGroup(ctx, rootGroup); err != nil {
		t.Fatalf("set local group inheritance: %v", err)
	}
	result, err = store.ImportCatalog(ctx, groups, hosts, rules)
	if err != nil {
		t.Fatalf("repeat catalog import: %v", err)
	}
	if result != (CatalogImportResult{}) {
		t.Fatalf("repeat import inserted records: %#v", result)
	}
	gateway, err = store.GetHost(ctx, "gateway")
	if err != nil || gateway.CredentialRef != "local-keychain-entry" || gateway.LastConnectedAt.IsZero() {
		t.Fatalf("repeat import changed local credential or recent metadata: %#v, %v", gateway, err)
	}
	rootGroup, err = store.GetGroup(ctx, "group-root")
	if err != nil || rootGroup.AuthProfileID != localProfile.Id {
		t.Fatalf("repeat import changed local group inheritance: %#v, %v", rootGroup, err)
	}
}

func TestImportCatalogRollsBackOnConflict(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "workbench.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateGroup(ctx, model.Group{Id: "existing", Name: "Already here"}); err != nil {
		t.Fatalf("create existing group: %v", err)
	}

	_, err = store.ImportCatalog(ctx,
		[]model.Group{
			{Id: "new-first", Name: "New first"},
			{Id: "new-conflict", Name: "Already here"},
		},
		[]model.Host{{Id: "new-host", Kind: model.HostKindSSH, Name: "new", IP: "192.0.2.5", Port: 22, GroupId: "new-first"}},
		nil,
	)
	if err == nil {
		t.Fatal("import unexpectedly accepted a unique group name conflict")
	}
	if _, err := store.GetGroup(ctx, "new-first"); err == nil {
		t.Fatal("failed import left a partially inserted group")
	}
	if _, err := store.GetHost(ctx, "new-host"); err == nil {
		t.Fatal("failed import left a partially inserted host")
	}
}

func TestImportCatalogRejectsInvalidJumpBeforeCommit(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "workbench.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	_, err = store.ImportCatalog(ctx,
		[]model.Group{{Id: "incoming-group", Name: "Incoming"}},
		[]model.Host{{
			Id: "incoming-host", Kind: model.HostKindSSH, Name: "incoming", IP: "192.0.2.5",
			Port: 22, GroupId: "incoming-group", ProxyJumpList: []string{"missing"},
		}}, nil,
	)
	if err == nil {
		t.Fatal("import unexpectedly accepted a missing ProxyJump host")
	}
	if _, err := store.GetGroup(ctx, "incoming-group"); err == nil {
		t.Fatal("invalid import committed its group")
	}
}

func TestImportCatalogRejectsProxyJumpCycles(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "workbench.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	_, err = store.ImportCatalog(ctx, nil, []model.Host{
		{Id: "jump-a", Kind: model.HostKindSSH, Name: "jump-a", IP: "192.0.2.10", Port: 22, ProxyJumpList: []string{"jump-b"}},
		{Id: "jump-b", Kind: model.HostKindSSH, Name: "jump-b", IP: "192.0.2.11", Port: 22, ProxyJumpList: []string{"jump-a"}},
	}, nil)
	if err == nil {
		t.Fatal("import unexpectedly accepted a circular ProxyJump chain")
	}
	if _, err := store.GetHost(ctx, "jump-a"); err == nil {
		t.Fatal("cyclic import committed hosts")
	}
}
