package sshclient

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"

	"tui-ssh-workbench/internal/model"
)

func TestKeyboardInteractiveHandlerSuspendsAndRestoresNetworkDeadline(t *testing.T) {
	connection := &recordingDeadlineConn{}
	host := model.Host{Id: "jump", Name: "gateway"}
	handler := keyboardInteractiveDeadlineHandler(connection, func(_ context.Context, gotHost model.Host, _, _ string, _ []string, _ []bool) ([]string, error) {
		if gotHost.Id != host.Id {
			t.Fatalf("challenge host = %q, want %q", gotHost.Id, host.Id)
		}
		if len(connection.deadlines) != 1 || !connection.deadlines[0].IsZero() {
			t.Fatalf("deadline during user prompt = %v, want no deadline", connection.deadlines)
		}
		return []string{"123456"}, nil
	}, 15*time.Second)
	answers, err := handler(context.Background(), host, "MFA", "", []string{"OTP"}, []bool{false})
	if err != nil {
		t.Fatalf("answer challenge: %v", err)
	}
	if !reflect.DeepEqual(answers, []string{"123456"}) {
		t.Fatalf("answers = %v, want OTP response", answers)
	}
	if len(connection.deadlines) != 2 || connection.deadlines[1].IsZero() || time.Until(connection.deadlines[1]) <= 0 {
		t.Fatalf("deadline after prompt = %v, want a future network deadline", connection.deadlines)
	}
}

func TestClassifyShellExitSeparatesCleanExitFromTransportLoss(t *testing.T) {
	transportError := errors.New("connection reset")
	tests := []struct {
		name     string
		waitErr  error
		probeErr error
		closed   bool
		wantEOF  bool
		wantLost bool
	}{
		{name: "zero exit", wantEOF: true},
		{name: "nonzero remote exit", waitErr: &ssh.ExitError{}, wantEOF: true},
		{name: "missing status but live SSH transport", waitErr: &ssh.ExitMissingError{}, wantEOF: true},
		{name: "missing status and dead SSH transport", waitErr: &ssh.ExitMissingError{}, probeErr: transportError, wantLost: true},
		{name: "I/O failure", waitErr: transportError, wantLost: true},
		{name: "explicitly closed session", waitErr: transportError, closed: true, wantEOF: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := classifyShellExit(test.waitErr, test.probeErr, test.closed)
			if test.wantEOF && !errors.Is(got, io.EOF) {
				t.Fatalf("classification = %v, want EOF", got)
			}
			if test.wantLost && !errors.Is(got, ErrTransportLost) {
				t.Fatalf("classification = %v, want ErrTransportLost", got)
			}
		})
	}
}

func TestKeyboardInteractiveHandlerRestoresDeadlineOnPromptError(t *testing.T) {
	connection := &recordingDeadlineConn{}
	wantErr := errors.New("prompt failed")
	handler := keyboardInteractiveDeadlineHandler(connection, func(context.Context, model.Host, string, string, []string, []bool) ([]string, error) {
		return nil, wantErr
	}, 15*time.Second)
	_, err := handler(context.Background(), model.Host{}, "MFA", "", nil, nil)
	if !errors.Is(err, wantErr) {
		t.Fatalf("handler error = %v, want %v", err, wantErr)
	}
	if len(connection.deadlines) != 2 || connection.deadlines[1].IsZero() {
		t.Fatalf("deadline after prompt error = %v, want network deadline restored", connection.deadlines)
	}
}

func TestConnectOptionsValidationAndDefaults(t *testing.T) {
	defaults := DefaultConnectOptions()
	if defaults.ConnectTimeout != 15*time.Second || defaults.KeepaliveInterval != 30*time.Second || defaults.KeepaliveTimeout != 10*time.Second {
		t.Fatalf("default SSH options = %#v", defaults)
	}
	if err := defaults.validate(); err != nil {
		t.Fatalf("default SSH options are invalid: %v", err)
	}
	disabledKeepalive := defaults
	disabledKeepalive.KeepaliveInterval = 0
	if err := disabledKeepalive.validate(); err != nil {
		t.Fatalf("disabled keepalive option should be valid: %v", err)
	}
	invalid := defaults
	invalid.ConnectTimeout = 0
	if err := invalid.validate(); err == nil {
		t.Fatal("zero SSH connect timeout was accepted")
	}
}

func TestVerifyHostKeyDistinguishesChangedFingerprintFromUnknown(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	oldKey, newKey := testHostKey(t), testHostKey(t)
	host := model.Host{Id: "device", Name: "device", IP: "192.0.2.20", Port: 33333}
	if err := TrustHost(host, oldKey); err != nil {
		t.Fatalf("trust prior key: %v", err)
	}
	entries, err := ListKnownHostEntries()
	if err != nil || len(entries) != 1 {
		t.Fatalf("list prior host keys = %#v, err=%v", entries, err)
	}
	knownHostsPath, err := ensureKnownHostsFile()
	if err != nil {
		t.Fatalf("known_hosts path: %v", err)
	}
	callback := ssh.HostKeyCallback(func(string, net.Addr, ssh.PublicKey) error {
		return &knownhosts.KeyError{Want: []knownhosts.KnownKey{{Key: oldKey, Filename: knownHostsPath, Line: entries[0].LineNumber}}}
	})
	wrapped := verifyHostKey(callback, "192.0.2.20:33333", host)
	err = wrapped("[192.0.2.20]:33333", nil, newKey)
	var changed *ChangedHostKeyError
	if !errors.As(err, &changed) {
		t.Fatalf("verification error = %T %v; want ChangedHostKeyError", err, err)
	}
	if changed.Fingerprint != ssh.FingerprintSHA256(newKey) || changed.Address != "192.0.2.20:33333" || changed.Host.Id != host.Id {
		t.Fatalf("changed host key details = %#v", changed)
	}
	if len(changed.PreviousFingerprints) != 1 || changed.PreviousFingerprints[0] != ssh.FingerprintSHA256(oldKey) {
		t.Fatalf("previous fingerprints = %v", changed.PreviousFingerprints)
	}
	if len(changed.References) != 1 || changed.References[0] != (KnownHostKeyReference{LineNumber: entries[0].LineNumber, Fingerprint: ssh.FingerprintSHA256(oldKey)}) || !changed.CanReplace {
		t.Fatalf("replacement references = %#v", changed.References)
	}

	unknownCallback := ssh.HostKeyCallback(func(string, net.Addr, ssh.PublicKey) error {
		return &knownhosts.KeyError{}
	})
	unknownErr := verifyHostKey(unknownCallback, "192.0.2.20:33333", host)("[192.0.2.20]:33333", nil, newKey)
	var unknown *UnknownHostKeyError
	if !errors.As(unknownErr, &unknown) {
		t.Fatalf("unknown host error = %T %v; want UnknownHostKeyError", unknownErr, unknownErr)
	}
}

func TestDialSSHClientHonorsConfiguredTimeout(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	accepted := make(chan net.Conn, 1)
	go func() {
		connection, acceptErr := listener.Accept()
		if acceptErr == nil {
			accepted <- connection
		}
	}()

	address := listener.Addr().(*net.TCPAddr)
	host := model.Host{Name: "slow SSH fixture", IP: "127.0.0.1", Port: address.Port, User: "test"}
	options := ConnectOptions{
		ConnectTimeout:    40 * time.Millisecond,
		KeepaliveInterval: 0,
		KeepaliveTimeout:  time.Second,
	}
	started := time.Now()
	_, err = dialSSHClient(context.Background(), host, nil, "", ssh.InsecureIgnoreHostKey(), nil, options)
	elapsed := time.Since(started)
	if err == nil {
		t.Fatal("SSH negotiation unexpectedly succeeded without a server banner")
	}
	if elapsed > time.Second {
		t.Fatalf("configured 40 ms SSH timeout took %v", elapsed)
	}
	select {
	case connection := <-accepted:
		_ = connection.Close()
	case <-time.After(time.Second):
		t.Fatal("SSH client did not reach the local fixture")
	}
}

type recordingDeadlineConn struct {
	net.Conn
	deadlines []time.Time
}

func (c *recordingDeadlineConn) SetDeadline(deadline time.Time) error {
	c.deadlines = append(c.deadlines, deadline)
	return nil
}

func TestResolveProxyChainSupportsMultipleHops(t *testing.T) {
	first := model.Host{Id: "first", Kind: model.HostKindSSH, Name: "gateway-a"}
	second := model.Host{Id: "second", Kind: model.HostKindSSH, Name: "gateway-b", ProxyJumpList: []string{first.Id}}
	target := model.Host{Id: "target", Kind: model.HostKindSSH, Name: "application", ProxyJumpList: []string{second.Id}}

	chain, err := resolveProxyChain(target, []model.Host{target, second, first})
	if err != nil {
		t.Fatalf("resolve multi-hop chain: %v", err)
	}
	got := make([]string, len(chain))
	for index, host := range chain {
		got[index] = host.Id
	}
	if want := []string{"first", "second", "target"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("resolved chain = %v, want %v", got, want)
	}
}

func TestResolveProxyChainRejectsInvalidReferencesAndCycles(t *testing.T) {
	t.Run("missing jump host", func(t *testing.T) {
		target := model.Host{Id: "target", Name: "application", ProxyJumpList: []string{"missing"}}
		if _, err := resolveProxyChain(target, nil); err == nil {
			t.Fatal("expected missing jump host to be rejected")
		}
	})

	t.Run("cycle", func(t *testing.T) {
		first := model.Host{Id: "first", Name: "gateway-a", ProxyJumpList: []string{"target"}}
		target := model.Host{Id: "target", Name: "application", ProxyJumpList: []string{"first"}}
		if _, err := resolveProxyChain(target, []model.Host{first, target}); err == nil {
			t.Fatal("expected circular jump references to be rejected")
		}
	})

	t.Run("local terminal is not an SSH hop", func(t *testing.T) {
		jump := model.Host{Id: "local", Kind: model.HostKindLocal, Name: "local shell"}
		target := model.Host{Id: "target", Name: "application", ProxyJumpList: []string{jump.Id}}
		if _, err := resolveProxyChain(target, []model.Host{jump}); err == nil {
			t.Fatal("expected local terminal jump host to be rejected")
		}
	})
}

func TestSOCKS5ForwardRejectsNonLoopbackListeners(t *testing.T) {
	if _, err := (&Session{}).ForwardSOCKS5("0.0.0.0:1080"); err == nil {
		t.Fatal("unauthenticated SOCKS5 proxy accepted a non-loopback listener")
	}
}

func TestConfiguredIdentityFileSupportsTildeAndEncryptedKeys(t *testing.T) {
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKeyWithPassphrase(privateKey, "custom identity", []byte("key-passphrase"))
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	keyPath := filepath.Join(home, ".ssh", "workbench_ed25519")
	if err := os.MkdirAll(filepath.Dir(keyPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)

	if signer, err := identityFileSigner("~/.ssh/workbench_ed25519", ""); err != nil || signer != nil {
		t.Fatalf("encrypted key without passphrase = signer:%v err:%v; want no signer and no error", signer != nil, err)
	}
	if signer, err := identityFileSigner("~/.ssh/workbench_ed25519", "wrong-passphrase"); err != nil || signer != nil {
		t.Fatalf("encrypted key with wrong passphrase = signer:%v err:%v; want no signer and no error", signer != nil, err)
	}
	signer, err := identityFileSigner("~/.ssh/workbench_ed25519", "key-passphrase")
	if err != nil || signer == nil {
		t.Fatalf("encrypted identity key = signer:%v err:%v; want signer", signer != nil, err)
	}
	expected, err := ssh.NewSignerFromKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(signer.PublicKey().Marshal(), expected.PublicKey().Marshal()) {
		t.Fatal("configured identity file returned a different key")
	}
	if _, err := identityFileSigner(filepath.Join(home, "missing-key"), ""); err == nil {
		t.Fatal("missing configured identity file was accepted")
	}
	if _, err := identityFileSigner(home, ""); err == nil {
		t.Fatal("directory was accepted as a configured private key")
	}
}

func TestSSHConnectAuthenticatesWithConfiguredIdentityFile(t *testing.T) {
	t.Setenv("SSH_AUTH_SOCK", "")
	_, clientPrivateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	_, hostPrivateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	clientSigner, err := ssh.NewSignerFromKey(clientPrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	hostSigner, err := ssh.NewSignerFromKey(hostPrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	privateKeyBlock, err := ssh.MarshalPrivateKeyWithPassphrase(clientPrivateKey, "test identity", []byte("key-passphrase"))
	if err != nil {
		t.Fatal(err)
	}
	identityPath := filepath.Join(t.TempDir(), "id_custom")
	if err := os.WriteFile(identityPath, pem.EncodeToMemory(privateKeyBlock), 0o600); err != nil {
		t.Fatal(err)
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	serverConfig := &ssh.ServerConfig{PublicKeyCallback: func(connection ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
		if connection.User() != "tester" || !bytes.Equal(key.Marshal(), clientSigner.PublicKey().Marshal()) {
			return nil, errors.New("unexpected SSH public key")
		}
		return nil, nil
	}}
	serverConfig.AddHostKey(hostSigner)
	serverResult := make(chan error, 1)
	go func() {
		connection, err := listener.Accept()
		if err != nil {
			serverResult <- err
			return
		}
		server, _, requests, err := ssh.NewServerConn(connection, serverConfig)
		if err == nil {
			go ssh.DiscardRequests(requests)
			_ = server.Close()
		}
		_ = connection.Close()
		serverResult <- err
	}()

	address := listener.Addr().(*net.TCPAddr)
	host := model.Host{
		Kind: model.HostKindSSH, Name: "custom-key", IP: "127.0.0.1", Port: address.Port,
		User: "tester", IdentityFile: identityPath,
	}
	client, err := dialSSHClient(
		context.Background(), host, nil, "key-passphrase", ssh.InsecureIgnoreHostKey(), nil, DefaultConnectOptions(),
	)
	if err != nil {
		t.Fatalf("connect with configured encrypted key: %v", err)
	}
	_ = client.Close()
	select {
	case err := <-serverResult:
		if err != nil {
			t.Fatalf("SSH server rejected configured key: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("SSH server did not finish the identity-file handshake")
	}
}

func TestConfiguredIdentityFilePairsOpenSSHUserCertificate(t *testing.T) {
	_, clientPrivateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	clientSigner, err := ssh.NewSignerFromKey(clientPrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	_, caPrivateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	caSigner, err := ssh.NewSignerFromKey(caPrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	identityPath := filepath.Join(t.TempDir(), "id_ed25519")
	privateKeyBlock, err := ssh.MarshalPrivateKey(clientPrivateKey, "test identity")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(identityPath, pem.EncodeToMemory(privateKeyBlock), 0o600); err != nil {
		t.Fatal(err)
	}
	certificate := &ssh.Certificate{
		Key: clientSigner.PublicKey(), CertType: ssh.UserCert, KeyId: "test-user",
		ValidPrincipals: []string{"tester"}, ValidAfter: uint64(time.Now().Add(-time.Minute).Unix()),
		ValidBefore: uint64(time.Now().Add(time.Minute).Unix()),
	}
	if err := certificate.SignCert(rand.Reader, caSigner); err != nil {
		t.Fatal(err)
	}
	certificatePath := identityPath + "-cert.pub"
	if err := os.WriteFile(certificatePath, ssh.MarshalAuthorizedKey(certificate), 0o600); err != nil {
		t.Fatal(err)
	}
	signer, err := identityFileSigner(identityPath, "")
	if err != nil || signer == nil {
		t.Fatalf("identity with OpenSSH user certificate = signer:%v err:%v", signer != nil, err)
	}
	loadedCertificate, ok := signer.PublicKey().(*ssh.Certificate)
	if !ok || loadedCertificate.CertType != ssh.UserCert || !bytes.Equal(loadedCertificate.SignatureKey.Marshal(), caSigner.PublicKey().Marshal()) {
		t.Fatalf("loaded identity did not use the signed user certificate: %#v", signer.PublicKey())
	}

	_, mismatchedPrivateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	mismatchedSigner, err := ssh.NewSignerFromKey(mismatchedPrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	mismatchedCertificate := &ssh.Certificate{
		Key: mismatchedSigner.PublicKey(), CertType: ssh.UserCert,
		ValidAfter: uint64(time.Now().Add(-time.Minute).Unix()), ValidBefore: uint64(time.Now().Add(time.Minute).Unix()),
	}
	if err := mismatchedCertificate.SignCert(rand.Reader, caSigner); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(certificatePath, ssh.MarshalAuthorizedKey(mismatchedCertificate), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := identityFileSigner(identityPath, ""); err == nil || !strings.Contains(err.Error(), "does not match private key") {
		t.Fatalf("mismatched user certificate error = %v", err)
	}

	hostCertificate := &ssh.Certificate{
		Key: clientSigner.PublicKey(), CertType: ssh.HostCert,
		ValidAfter: uint64(time.Now().Add(-time.Minute).Unix()), ValidBefore: uint64(time.Now().Add(time.Minute).Unix()),
	}
	if err := hostCertificate.SignCert(rand.Reader, caSigner); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(certificatePath, ssh.MarshalAuthorizedKey(hostCertificate), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := identityFileSigner(identityPath, ""); err == nil || !strings.Contains(err.Error(), "not a user certificate") {
		t.Fatalf("host certificate was not rejected: %v", err)
	}
}

func TestSSHConnectAuthenticatesWithOpenSSHUserCertificate(t *testing.T) {
	t.Setenv("SSH_AUTH_SOCK", "")
	t.Setenv("HOME", t.TempDir())
	_, clientPrivateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	clientSigner, err := ssh.NewSignerFromKey(clientPrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	_, caPrivateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	caSigner, err := ssh.NewSignerFromKey(caPrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	certificate := &ssh.Certificate{
		Key: clientSigner.PublicKey(), CertType: ssh.UserCert, KeyId: "workbench-test",
		ValidPrincipals: []string{"tester"}, ValidAfter: uint64(time.Now().Add(-time.Minute).Unix()),
		ValidBefore: uint64(time.Now().Add(time.Minute).Unix()),
	}
	if err := certificate.SignCert(rand.Reader, caSigner); err != nil {
		t.Fatal(err)
	}
	identityPath := filepath.Join(t.TempDir(), "id_ed25519")
	privateKeyBlock, err := ssh.MarshalPrivateKey(clientPrivateKey, "test identity")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(identityPath, pem.EncodeToMemory(privateKeyBlock), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(identityPath+"-cert.pub", ssh.MarshalAuthorizedKey(certificate), 0o600); err != nil {
		t.Fatal(err)
	}

	_, hostPrivateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	hostSigner, err := ssh.NewSignerFromKey(hostPrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	checker := &ssh.CertChecker{IsUserAuthority: func(authority ssh.PublicKey) bool {
		return bytes.Equal(authority.Marshal(), caSigner.PublicKey().Marshal())
	}}
	serverConfig := &ssh.ServerConfig{PublicKeyCallback: checker.Authenticate}
	serverConfig.AddHostKey(hostSigner)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	serverResult := make(chan error, 1)
	go func() {
		connection, err := listener.Accept()
		if err != nil {
			serverResult <- err
			return
		}
		server, _, requests, err := ssh.NewServerConn(connection, serverConfig)
		if err == nil {
			go ssh.DiscardRequests(requests)
			_ = server.Close()
		}
		_ = connection.Close()
		serverResult <- err
	}()

	address := listener.Addr().(*net.TCPAddr)
	host := model.Host{Kind: model.HostKindSSH, Name: "ca-user", IP: "127.0.0.1", Port: address.Port, User: "tester", IdentityFile: identityPath}
	client, err := dialSSHClient(context.Background(), host, nil, "", ssh.InsecureIgnoreHostKey(), nil, DefaultConnectOptions())
	if err != nil {
		t.Fatalf("SSH certificate authentication: %v", err)
	}
	_ = client.Close()
	select {
	case err := <-serverResult:
		if err != nil {
			t.Fatalf("SSH server rejected the CA-signed user certificate: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("SSH server did not finish the certificate-authenticated handshake")
	}
}
