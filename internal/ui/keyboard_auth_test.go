package ui

import (
	"context"
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"tui-ssh-workbench/internal/infra/sshclient"
	"tui-ssh-workbench/internal/model"
)

func TestKeyboardInteractivePromptCollectsEchoAndSecretAnswers(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	host := model.Host{Kind: model.HostKindSSH, Name: "mfa-host", IP: "10.0.0.8", Port: 22, User: "tester"}
	flow := newKeyboardAuthFlow(ctx)
	result := make(chan keyboardAuthResponse, 1)
	go func() {
		answers, err := flow.challenge(ctx, host, "MFA", "Enter the requested details", []string{"Account:", "One-time code:"}, []bool{true, false})
		result <- keyboardAuthResponse{answers: answers, err: err}
	}()

	state := modelState{
		width: 80, height: 24,
		connectingHost:   &host,
		connectCancel:    cancel,
		keyboardAuthFlow: flow,
	}
	updated, _ := state.Update(waitKeyboardAuthPromptCmd(flow)())
	state = unwrapModel(t, updated)
	if state.keyboardAuthPrompt == nil || state.keyboardAuthPrompt.question != 0 {
		t.Fatalf("first challenge question was not shown: %#v", state.keyboardAuthPrompt)
	}
	state = typeKeyboardAnswer(t, state, "operator")
	updated, _ = state.updateKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	state = unwrapModel(t, updated)
	if state.keyboardAuthPrompt == nil || state.keyboardAuthPrompt.question != 1 {
		t.Fatalf("second challenge question was not shown: %#v", state.keyboardAuthPrompt)
	}
	state = typeKeyboardAnswer(t, state, "654321")
	if strings.Contains(state.view(), "654321") {
		t.Fatal("non-echo challenge response was visible in the UI")
	}
	updated, command := state.updateKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	state = unwrapModel(t, updated)
	if state.keyboardAuthPrompt != nil || command == nil {
		t.Fatalf("completed challenge did not clear prompt and resume waiting: prompt=%#v command=%v", state.keyboardAuthPrompt, command != nil)
	}
	got := <-result
	if got.err != nil || strings.Join(got.answers, ",") != "operator,654321" {
		t.Fatalf("keyboard-interactive answers = %v, err=%v", got.answers, got.err)
	}
}

func TestKeyboardInteractivePromptEscapeCancelsConnection(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	host := model.Host{Kind: model.HostKindSSH, Name: "mfa-host", IP: "10.0.0.8", Port: 22, User: "tester"}
	flow := newKeyboardAuthFlow(ctx)
	result := make(chan error, 1)
	go func() {
		_, err := flow.challenge(ctx, host, "MFA", "", []string{"One-time code:"}, []bool{false})
		result <- err
	}()
	state := modelState{connectingHost: &host, connectCancel: cancel, keyboardAuthFlow: flow}
	updated, _ := state.Update(waitKeyboardAuthPromptCmd(flow)())
	state = unwrapModel(t, updated)
	updated, _ = state.updateKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
	state = unwrapModel(t, updated)
	if state.connectingHost != nil || state.keyboardAuthPrompt != nil || state.keyboardAuthFlow != nil {
		t.Fatal("Escape did not clear the interactive authentication state")
	}
	if err := <-result; !errors.Is(err, errKeyboardAuthCancelled) {
		t.Fatalf("challenge returned %v, want cancellation", err)
	}
}

func TestPasswordPromptIdentifiesAndStoresCredentialForJumpHost(t *testing.T) {
	jump := model.Host{Id: "jump-1", Kind: model.HostKindSSH, Name: "gateway", IP: "10.0.0.2", Port: 22, User: "jump-user"}
	target := model.Host{Id: "target-1", Kind: model.HostKindSSH, Name: "application", IP: "10.0.0.8", Port: 22, User: "app-user"}
	connection := connectionTarget{kind: newTabTarget, tabID: "tab-1", paneID: "pane-1"}
	state := modelState{
		width: 80, height: 24,
		connectingHost: &target, connectionTarget: &connection,
	}
	updated, _ := state.Update(terminalConnectedMsg{
		host: target, authenticationHost: jump, target: connection,
		err: &sshclient.AuthenticationError{Host: jump, Cause: errors.New("authentication rejected")},
	})
	state = unwrapModel(t, updated)
	if !state.passwordPrompt || state.authenticationHost == nil || state.authenticationHost.Id != jump.Id {
		t.Fatalf("password prompt host = %#v, want jump host %q", state.authenticationHost, jump.Id)
	}
	if view := state.passwordView(); !strings.Contains(view, "gateway") || !strings.Contains(view, "跳板机") {
		t.Fatalf("password prompt does not identify the jump host:\n%s", view)
	}
	state = typeKeyboardAnswer(t, state, "jump-secret")
	updated, command := state.updateKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	state = unwrapModel(t, updated)
	if command == nil || state.connectionPasswords[jump.Id] != "jump-secret" {
		t.Fatalf("jump credential was not retained for this retry: passwords=%v command=%v", state.connectionPasswords, command != nil)
	}
	if _, leaked := state.connectionPasswords[target.Id]; leaked {
		t.Fatal("jump credential was stored under the target host")
	}
	state.cancelSSHConnect()
}

func typeKeyboardAnswer(t *testing.T, state modelState, value string) modelState {
	t.Helper()
	for _, char := range value {
		updated, _ := state.updateKey(tea.KeyPressMsg(tea.Key{Text: string(char), Code: char}))
		state = unwrapModel(t, updated)
	}
	return state
}
