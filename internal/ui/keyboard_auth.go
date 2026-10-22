package ui

import (
	"context"
	"errors"

	tea "charm.land/bubbletea/v2"
	"tui-ssh-workbench/internal/model"
)

type keyboardAuthRequest struct {
	host        model.Host
	name        string
	instruction string
	questions   []string
	echos       []bool
	response    chan keyboardAuthResponse
}

type keyboardAuthResponse struct {
	answers []string
	err     error
}

type keyboardAuthFlow struct {
	ctx      context.Context
	requests chan keyboardAuthRequest
}

type keyboardAuthPromptState struct {
	request  keyboardAuthRequest
	question int
	value    string
	answers  []string
}

type keyboardAuthPromptMsg struct {
	flow    *keyboardAuthFlow
	request keyboardAuthRequest
}

func newKeyboardAuthFlow(ctx context.Context) *keyboardAuthFlow {
	return &keyboardAuthFlow{ctx: ctx, requests: make(chan keyboardAuthRequest, 1)}
}

func (flow *keyboardAuthFlow) challenge(ctx context.Context, host model.Host, name, instruction string, questions []string, echos []bool) ([]string, error) {
	request := keyboardAuthRequest{
		host: host, name: name, instruction: instruction,
		questions: append([]string(nil), questions...),
		echos:     append([]bool(nil), echos...),
		response:  make(chan keyboardAuthResponse, 1),
	}
	select {
	case flow.requests <- request:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	select {
	case response := <-request.response:
		return response.answers, response.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func waitKeyboardAuthPromptCmd(flow *keyboardAuthFlow) tea.Cmd {
	if flow == nil {
		return nil
	}
	return func() tea.Msg {
		select {
		case request := <-flow.requests:
			return keyboardAuthPromptMsg{flow: flow, request: request}
		case <-flow.ctx.Done():
			return nil
		}
	}
}

func newKeyboardAuthPromptState(request keyboardAuthRequest) *keyboardAuthPromptState {
	question := 0
	if len(request.questions) == 0 {
		question = -1
	}
	return &keyboardAuthPromptState{
		request: request, question: question,
		answers: make([]string, 0, len(request.questions)),
	}
}

func (m *modelState) clearKeyboardAuthPrompt() {
	if m.keyboardAuthPrompt != nil {
		for index := range m.keyboardAuthPrompt.answers {
			m.keyboardAuthPrompt.answers[index] = ""
		}
		m.keyboardAuthPrompt.answers = nil
		m.keyboardAuthPrompt.value = ""
		m.keyboardAuthPrompt = nil
	}
}

func (m *modelState) updateKeyboardAuthKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	prompt := m.keyboardAuthPrompt
	if prompt == nil {
		return m, nil
	}
	key := msg.Key()
	switch msg.String() {
	case "esc", "escape", "ctrl+c":
		select {
		case prompt.request.response <- keyboardAuthResponse{err: errKeyboardAuthCancelled}:
		default:
		}
		m.cancelSSHConnect()
		m.statusText = "已取消 SSH 交互认证"
		m.statusErr = false
		return m, nil
	case "enter":
		if prompt.question >= 0 {
			prompt.answers = append(prompt.answers, prompt.value)
			prompt.value = ""
			if prompt.question+1 < len(prompt.request.questions) {
				prompt.question++
				return m, nil
			}
		}
		answers := append([]string(nil), prompt.answers...)
		for index := range prompt.answers {
			prompt.answers[index] = ""
		}
		prompt.answers = nil
		prompt.value = ""
		request := prompt.request
		flow := m.keyboardAuthFlow
		m.keyboardAuthPrompt = nil
		select {
		case request.response <- keyboardAuthResponse{answers: answers}:
		default:
			return m, nil
		}
		return m, waitKeyboardAuthPromptCmd(flow)
	case "backspace", "ctrl+h":
		prompt.value = removeLastRune(prompt.value)
		return m, nil
	}
	if key.Text != "" && len([]rune(prompt.value))+len([]rune(key.Text)) <= 512 {
		prompt.value += key.Text
	}
	return m, nil
}

func (m *modelState) appendKeyboardAuthPaste(content string) {
	if m.keyboardAuthPrompt == nil {
		return
	}
	prompt := m.keyboardAuthPrompt
	if len([]rune(prompt.value))+len([]rune(content)) <= 512 {
		prompt.value += content
	}
}

func removeLastRune(value string) string {
	runes := []rune(value)
	if len(runes) == 0 {
		return value
	}
	return string(runes[:len(runes)-1])
}

var errKeyboardAuthCancelled = errors.New("SSH 交互认证已取消")
