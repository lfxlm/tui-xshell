package ui

import (
	"fmt"
	"regexp"
	"time"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"tui-ssh-workbench/internal/model"
)

const (
	terminalTriggerMatchBuffer = 8 << 10
	terminalTriggerCooldown    = 2 * time.Second
)

type compiledTerminalTrigger struct {
	name       string
	pattern    *regexp.Regexp
	action     string
	text       string
	sendEnter  bool
	scriptPath string
}

type terminalTriggerRuleState struct {
	rule         compiledTerminalTrigger
	lastMatchEnd uint64
	lastActionAt time.Time
}

type terminalTriggerRuntime struct {
	rules      []terminalTriggerRuleState
	buffer     string
	totalBytes uint64
	stripper   ansiTextStripper
	generation uint64
}

type terminalTriggerMatch struct {
	rule compiledTerminalTrigger
}

type terminalTriggerSentMsg struct {
	paneID     string
	generation uint64
	ruleName   string
	err        error
}

type terminalTriggerScriptLoadedMsg struct {
	paneID     string
	generation uint64
	ruleName   string
	script     *startupLoginScript
	err        error
}

func compileTerminalTriggers(rules []model.TerminalTriggerRule) []compiledTerminalTrigger {
	compiled := make([]compiledTerminalTrigger, 0, len(rules))
	for _, rule := range rules {
		pattern := rule.Pattern
		if rule.IgnoreCase {
			pattern = "(?i)" + pattern
		}
		expression, err := regexp.Compile(pattern)
		if err != nil {
			continue // Config validation rejects this before the UI is created.
		}
		compiled = append(compiled, compiledTerminalTrigger{
			name:       rule.Name,
			pattern:    expression,
			action:     rule.Action,
			text:       rule.Text,
			sendEnter:  rule.SendEnter,
			scriptPath: rule.ScriptPath,
		})
	}
	return compiled
}

func newTerminalTriggerRuntime(rules []compiledTerminalTrigger, generation uint64) *terminalTriggerRuntime {
	if len(rules) == 0 {
		return nil
	}
	states := make([]terminalTriggerRuleState, len(rules))
	for index, rule := range rules {
		states[index].rule = rule
	}
	return &terminalTriggerRuntime{rules: states, generation: generation}
}

// match strips VT control sequences and keeps a bounded rolling window so a
// prompt split across SSH reads can still match. Each non-overlapping match is
// consumed once; a short per-rule cooldown prevents automatic send loops.
func (runtime *terminalTriggerRuntime) match(data []byte, now time.Time) []terminalTriggerMatch {
	if runtime == nil || len(data) == 0 {
		return nil
	}
	text := runtime.stripper.write(data)
	if text == "" {
		return nil
	}
	runtime.totalBytes += uint64(len(text))
	runtime.buffer += text
	if len(runtime.buffer) > terminalTriggerMatchBuffer {
		cut := len(runtime.buffer) - terminalTriggerMatchBuffer
		for cut < len(runtime.buffer) && !utf8.RuneStart(runtime.buffer[cut]) {
			cut++
		}
		runtime.buffer = runtime.buffer[cut:]
	}
	baseOffset := runtime.totalBytes - uint64(len(runtime.buffer))

	var matches []terminalTriggerMatch
	for index := range runtime.rules {
		state := &runtime.rules[index]
		locations := state.rule.pattern.FindAllStringIndex(runtime.buffer, -1)
		var candidate bool
		for _, location := range locations {
			start := baseOffset + uint64(location[0])
			end := baseOffset + uint64(location[1])
			if end <= state.lastMatchEnd {
				continue
			}
			if state.lastMatchEnd != 0 && start < state.lastMatchEnd {
				// A greedy expression can grow as more bytes arrive. Treat it as
				// the same match instead of firing again for its longer suffix.
				state.lastMatchEnd = end
				continue
			}
			if !candidate {
				candidate = true
				if state.lastActionAt.IsZero() || now.Sub(state.lastActionAt) >= terminalTriggerCooldown {
					matches = append(matches, terminalTriggerMatch{rule: state.rule})
					state.lastActionAt = now
				}
			}
			state.lastMatchEnd = end
		}
	}
	return matches
}

func terminalTriggerActionsCmd(session terminalSession, paneID string, runtime *terminalTriggerRuntime, encodingName string, matches []terminalTriggerMatch) tea.Cmd {
	if session == nil || runtime == nil || len(matches) == 0 {
		return nil
	}
	var sends []terminalTriggerMatch
	for _, match := range matches {
		if match.rule.action == model.TerminalTriggerSend {
			sends = append(sends, match)
		}
	}
	if len(sends) == 0 {
		return nil
	}
	generation := runtime.generation
	return func() tea.Msg {
		for _, match := range sends {
			text := []byte(match.rule.text)
			if match.rule.sendEnter {
				text = append(text, '\r')
			}
			payload, err := encodeTerminalText(encodingName, text)
			for index := range text {
				text[index] = 0
			}
			if err == nil {
				err = session.Write(payload)
			}
			for index := range payload {
				payload[index] = 0
			}
			if err != nil {
				return terminalTriggerSentMsg{paneID: paneID, generation: generation, ruleName: match.rule.name, err: err}
			}
		}
		last := sends[len(sends)-1]
		return terminalTriggerSentMsg{paneID: paneID, generation: generation, ruleName: last.rule.name}
	}
}

func terminalTriggerLoadScriptCmd(paneID string, generation uint64, ruleName, scriptPath string) tea.Cmd {
	return func() tea.Msg {
		script, err := loadStartupLoginScript(scriptPath)
		if err == nil && script == nil {
			err = fmt.Errorf("触发脚本为空")
		}
		return terminalTriggerScriptLoadedMsg{
			paneID: paneID, generation: generation, ruleName: ruleName, script: script, err: err,
		}
	}
}

func terminalTriggerStatus(match terminalTriggerMatch) string {
	return fmt.Sprintf("触发器「%s」：%s", match.rule.name, match.rule.text)
}

func appendTerminalTriggerPendingOutput(current, incoming []byte) []byte {
	if len(incoming) >= terminalTriggerMatchBuffer {
		incoming = incoming[len(incoming)-terminalTriggerMatchBuffer:]
		for len(incoming) > 0 && !utf8.RuneStart(incoming[0]) {
			incoming = incoming[1:]
		}
		return append([]byte(nil), incoming...)
	}
	current = append(current, incoming...)
	if len(current) > terminalTriggerMatchBuffer {
		cut := len(current) - terminalTriggerMatchBuffer
		for cut < len(current) && !utf8.RuneStart(current[cut]) {
			cut++
		}
		current = append([]byte(nil), current[cut:]...)
	}
	return current
}

func (m *modelState) handleTerminalTriggerMatches(pane *terminalPane, matches []terminalTriggerMatch, output []byte) tea.Cmd {
	if pane == nil || len(matches) == 0 {
		return nil
	}
	var bellCmd tea.Cmd
	var scriptCmd tea.Cmd
	var sendable []terminalTriggerMatch
	triggerScriptMatched := false
	for _, match := range matches {
		if match.rule.action == model.TerminalTriggerScript {
			triggerScriptMatched = true
			break
		}
	}
	for _, match := range matches {
		switch match.rule.action {
		case model.TerminalTriggerSend:
			// Do not interleave trigger writes with a host startup command or a
			// prompt-driven login script on the same SSH channel.
			if pane.startupPending != "" || pane.startupScript != nil || pane.triggerScriptLoading || triggerScriptMatched {
				reason := "自动登录仍在运行"
				if triggerScriptMatched {
					reason = "同批触发脚本正在启动"
				} else if pane.triggerScriptLoading {
					reason = "触发脚本正在加载"
				}
				pane.statusText = fmt.Sprintf("触发器「%s」已匹配；%s，跳过发送", match.rule.name, reason)
				pane.statusErr = false
				continue
			}
			sendable = append(sendable, match)
		case model.TerminalTriggerMessage:
			pane.statusText = terminalTriggerStatus(match)
			pane.statusErr = false
		case model.TerminalTriggerScript:
			if pane.startupPending != "" || pane.startupScript != nil || pane.triggerScriptLoading || scriptCmd != nil {
				pane.statusText = fmt.Sprintf("触发器「%s」已匹配；已有登录或触发脚本运行，跳过启动", match.rule.name)
				pane.statusErr = false
				continue
			}
			pane.triggerScriptLoading = true
			pane.triggerScriptOutput = appendTerminalTriggerPendingOutput(nil, output)
			pane.statusText = fmt.Sprintf("正在加载触发器「%s」的脚本…", match.rule.name)
			pane.statusErr = false
			scriptCmd = terminalTriggerLoadScriptCmd(pane.id, pane.triggerRuntime.generation, match.rule.name, match.rule.scriptPath)
		case model.TerminalTriggerBell:
			if m.config.Terminal.WithDefaults().Bell == model.TerminalBellVisual {
				pane.bellUntil = time.Now().Add(800 * time.Millisecond)
				paneID := pane.id
				bellCmd = tea.Tick(800*time.Millisecond, func(time.Time) tea.Msg {
					return terminalBellExpiredMsg{paneID: paneID}
				})
			}
		}
	}
	sendCmd := terminalTriggerActionsCmd(pane.session, pane.id, pane.triggerRuntime, pane.terminalInputEncoding, sendable)
	return tea.Batch(sendCmd, bellCmd, scriptCmd)
}
