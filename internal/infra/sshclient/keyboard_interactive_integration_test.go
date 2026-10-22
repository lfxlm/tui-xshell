package sshclient

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"errors"
	"net"
	"reflect"
	"sync/atomic"
	"testing"

	"golang.org/x/crypto/ssh"
	"tui-ssh-workbench/internal/model"
)

func TestConnectSFTPWithKeyboardInteractiveSupportsMultipleRounds(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv("HOME", configHome)
	workingDirectory := t.TempDir()

	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate test host key: %v", err)
	}
	signer, err := ssh.NewSignerFromKey(privateKey)
	if err != nil {
		t.Fatalf("create test host signer: %v", err)
	}
	serverConfig := &ssh.ServerConfig{
		KeyboardInteractiveCallback: func(_ ssh.ConnMetadata, challenge ssh.KeyboardInteractiveChallenge) (*ssh.Permissions, error) {
			passwordAnswers, err := challenge("Password", "Enter the account password", []string{"Password:"}, []bool{false})
			if err != nil {
				return nil, err
			}
			if !reflect.DeepEqual(passwordAnswers, []string{"hop-secret"}) {
				return nil, ssh.ErrNoAuth
			}
			otpAnswers, err := challenge("MFA", "Enter the one-time code", []string{"Verification code:"}, []bool{false})
			if err != nil {
				return nil, err
			}
			if !reflect.DeepEqual(otpAnswers, []string{"123456"}) {
				return nil, ssh.ErrNoAuth
			}
			return &ssh.Permissions{}, nil
		},
	}
	serverConfig.AddHostKey(signer)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen for keyboard-interactive SSH: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			connection, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}
			go serveSFTPTestConnection(connection, serverConfig, workingDirectory, make(chan string, 1))
		}
	}()

	host := model.Host{
		Kind: model.HostKindSSH, Name: "mfa-test", IP: "127.0.0.1",
		Port: listener.Addr().(*net.TCPAddr).Port, User: "tester",
	}
	if err := TrustHost(host, signer.PublicKey()); err != nil {
		t.Fatalf("trust test host: %v", err)
	}
	observed := make(chan string, 2)
	handler := func(_ context.Context, promptedHost model.Host, name, instruction string, questions []string, echos []bool) ([]string, error) {
		if promptedHost.Name != host.Name {
			t.Errorf("challenge host = %q, want %q", promptedHost.Name, host.Name)
		}
		switch name {
		case "Password":
			if instruction != "Enter the account password" || !reflect.DeepEqual(questions, []string{"Password:"}) || !reflect.DeepEqual(echos, []bool{false}) {
				t.Errorf("password challenge payload mismatch: instruction=%q questions=%v echos=%v", instruction, questions, echos)
			}
			observed <- name
			return []string{"hop-secret"}, nil
		case "MFA":
			if instruction != "Enter the one-time code" || !reflect.DeepEqual(questions, []string{"Verification code:"}) || !reflect.DeepEqual(echos, []bool{false}) {
				t.Errorf("MFA challenge payload mismatch: instruction=%q questions=%v echos=%v", instruction, questions, echos)
			}
			observed <- name
			return []string{"123456"}, nil
		default:
			return nil, ssh.ErrNoAuth
		}
	}

	session, err := ConnectSFTPWithJumpsAndKeyboardInteractive(context.Background(), host, "", nil, handler)
	if err != nil {
		t.Fatalf("connect using keyboard-interactive MFA: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	client, err := session.OpenSFTP()
	if err != nil {
		t.Fatalf("open SFTP after MFA: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	remotePath, err := client.WorkingDirectory()
	if err != nil {
		t.Fatalf("read working directory after MFA: %v", err)
	}
	if _, err := client.List(remotePath); err != nil {
		t.Fatalf("list SFTP directory after MFA: %v", err)
	}
	for _, expected := range []string{"Password", "MFA"} {
		select {
		case got := <-observed:
			if got != expected {
				t.Fatalf("challenge order = %q, want %q", got, expected)
			}
		}
	}
}

func TestConnectSFTPWithKeyboardInteractiveAuthenticatesJumpHost(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv("HOME", configHome)
	targetListener, targetSigner, _ := newSFTPTestServer(t, t.TempDir())
	jumpListener, jumpSigner, _ := newProxyJumpTestServerConfigured(t, func(config *ssh.ServerConfig) {
		config.KeyboardInteractiveCallback = func(_ ssh.ConnMetadata, challenge ssh.KeyboardInteractiveChallenge) (*ssh.Permissions, error) {
			answers, err := challenge("Jump MFA", "Approve gateway access", []string{"Gateway code:"}, []bool{false})
			if err != nil {
				return nil, err
			}
			if !reflect.DeepEqual(answers, []string{"gateway-otp"}) {
				return nil, ssh.ErrNoAuth
			}
			return &ssh.Permissions{}, nil
		}
	})
	jump := model.Host{
		Id: "jump", Kind: model.HostKindSSH, Name: "gateway", IP: "127.0.0.1",
		Port: jumpListener.Addr().(*net.TCPAddr).Port, User: "jump-user",
	}
	target := model.Host{
		Id: "target", Kind: model.HostKindSSH, Name: "application", IP: "127.0.0.1",
		Port: targetListener.Addr().(*net.TCPAddr).Port, User: "app-user", ProxyJumpList: []string{jump.Id},
	}
	if err := TrustHost(jump, jumpSigner.PublicKey()); err != nil {
		t.Fatalf("trust jump host: %v", err)
	}
	if err := TrustHost(target, targetSigner.PublicKey()); err != nil {
		t.Fatalf("trust target host: %v", err)
	}
	prompted := make(chan struct{}, 1)
	handler := func(_ context.Context, host model.Host, name, instruction string, questions []string, echos []bool) ([]string, error) {
		if host.Id != jump.Id || name != "Jump MFA" || instruction != "Approve gateway access" ||
			!reflect.DeepEqual(questions, []string{"Gateway code:"}) || !reflect.DeepEqual(echos, []bool{false}) {
			t.Errorf("unexpected jump challenge: host=%q name=%q instruction=%q questions=%v echos=%v", host.Id, name, instruction, questions, echos)
		}
		prompted <- struct{}{}
		return []string{"gateway-otp"}, nil
	}
	session, err := ConnectSFTPWithJumpsAndKeyboardInteractive(context.Background(), target, "", []model.Host{jump, target}, handler)
	if err != nil {
		t.Fatalf("connect through keyboard-interactive jump host: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	client, err := session.OpenSFTP()
	if err != nil {
		t.Fatalf("open target SFTP after jump authentication: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	remotePath, err := client.WorkingDirectory()
	if err != nil {
		t.Fatalf("read target SFTP working directory: %v", err)
	}
	if _, err := client.List(remotePath); err != nil {
		t.Fatalf("list target SFTP directory: %v", err)
	}
	select {
	case <-prompted:
	default:
		t.Fatal("keyboard-interactive handler was not called for the jump host")
	}
}

func TestConnectSFTPWithDifferentPasswordsForJumpAndTarget(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv("HOME", configHome)
	workingDirectory := t.TempDir()
	var jumpAttempts atomic.Int32
	jumpListener, jumpSigner, _ := newProxyJumpTestServerConfigured(t, func(config *ssh.ServerConfig) {
		config.PasswordCallback = func(connection ssh.ConnMetadata, password []byte) (*ssh.Permissions, error) {
			jumpAttempts.Add(1)
			if connection.User() != "jump-user" || string(password) != "jump-secret" {
				return nil, ssh.ErrNoAuth
			}
			return &ssh.Permissions{}, nil
		}
	})
	targetListener, targetSigner, _ := newPasswordSFTPTestServer(t, workingDirectory, "target-user", "target-secret")
	jump := model.Host{
		Id: "jump-auth", Kind: model.HostKindSSH, Name: "gateway", IP: "127.0.0.1",
		Port: jumpListener.Addr().(*net.TCPAddr).Port, User: "jump-user",
	}
	target := model.Host{
		Id: "target-auth", Kind: model.HostKindSSH, Name: "application", IP: "127.0.0.1",
		Port: targetListener.Addr().(*net.TCPAddr).Port, User: "target-user", ProxyJumpList: []string{jump.Id},
	}
	if err := TrustHost(jump, jumpSigner.PublicKey()); err != nil {
		t.Fatalf("trust jump host: %v", err)
	}
	if err := TrustHost(target, targetSigner.PublicKey()); err != nil {
		t.Fatalf("trust target host: %v", err)
	}
	hosts := []model.Host{jump, target}
	_, err := ConnectSFTPWithJumpsAndCredentials(context.Background(), target, map[string]string{
		HostCredentialKey(jump): "incorrect-secret",
	}, hosts, nil)
	var authErr *AuthenticationError
	if !errors.As(err, &authErr) || authErr.Host.Id != jump.Id {
		t.Fatalf("authentication error should identify the jump host %q, got %v / %#v", jump.Id, err, authErr)
	}
	_, err = ConnectSFTPWithJumpsAndCredentials(context.Background(), target, map[string]string{
		HostCredentialKey(jump): "jump-secret",
	}, hosts, nil)
	if !errors.As(err, &authErr) || authErr.Host.Id != target.Id {
		t.Fatalf("after authenticating the jump, the next error should identify target %q, got %v / %#v", target.Id, err, authErr)
	}

	credentials := map[string]string{
		HostCredentialKey(jump):   "jump-secret",
		HostCredentialKey(target): "target-secret",
	}
	session, err := ConnectSFTPWithJumpsAndCredentials(context.Background(), target, credentials, hosts, nil)
	if err != nil {
		t.Fatalf("connect with separate jump and target passwords: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	client, err := session.OpenSFTP()
	if err != nil {
		t.Fatalf("open target SFTP subsystem: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	remotePath, err := client.WorkingDirectory()
	if err != nil {
		t.Fatalf("read target working directory: %v", err)
	}
	if _, err := client.List(remotePath); err != nil {
		t.Fatalf("list target directory: %v", err)
	}
	if jumpAttempts.Load() < 2 {
		t.Fatalf("jump password callback attempts = %d, want at least 2", jumpAttempts.Load())
	}
}

func newPasswordSFTPTestServer(t *testing.T, workingDirectory, username, password string) (net.Listener, ssh.Signer, <-chan string) {
	t.Helper()
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate target host key: %v", err)
	}
	signer, err := ssh.NewSignerFromKey(privateKey)
	if err != nil {
		t.Fatalf("create target host signer: %v", err)
	}
	serverConfig := &ssh.ServerConfig{PasswordCallback: func(connection ssh.ConnMetadata, provided []byte) (*ssh.Permissions, error) {
		if connection.User() != username || string(provided) != password {
			return nil, ssh.ErrNoAuth
		}
		return &ssh.Permissions{}, nil
	}}
	serverConfig.AddHostKey(signer)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen for password-protected target SSH: %v", err)
	}
	requests := make(chan string, 8)
	go func() {
		for {
			connection, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}
			go serveSFTPTestConnection(connection, serverConfig, workingDirectory, requests)
		}
	}()
	t.Cleanup(func() { _ = listener.Close() })
	return listener, signer, requests
}
