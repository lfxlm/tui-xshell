package ui

import (
	"context"
	"errors"
	"fmt"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"golang.org/x/text/transform"

	"tui-ssh-workbench/internal/model"
)

func normalizedTerminalEncoding(name string) string {
	if model.IsTerminalEncodingSupported(name) {
		return name
	}
	return model.TerminalEncodingUTF8
}

func effectiveTerminalInputEncoding(host model.Host) string {
	if model.IsTerminalEncodingSupported(host.TerminalInputEncoding) {
		return host.TerminalInputEncoding
	}
	return normalizedTerminalEncoding(host.TerminalEncoding)
}

func terminalEncodingSummary(pane *terminalPane) string {
	if pane == nil {
		return model.TerminalEncodingUTF8
	}
	output := normalizedTerminalEncoding(pane.terminalEncoding)
	input := normalizedTerminalEncoding(pane.terminalInputEncoding)
	if input == output {
		return output
	}
	return "显示 " + output + " / 输入 " + input
}

func encodeTerminalInput(pane *terminalPane, input []byte) ([]byte, error) {
	encodingName := model.TerminalEncodingUTF8
	if pane != nil {
		encodingName = normalizedTerminalEncoding(pane.terminalInputEncoding)
		if pane.terminalInputEncoding == "" {
			encodingName = normalizedTerminalEncoding(pane.terminalEncoding)
		}
	}
	return encodeTerminalText(encodingName, input)
}

func encodeTerminalText(encodingName string, input []byte) ([]byte, error) {
	encodingName = normalizedTerminalEncoding(encodingName)
	if encodingName == model.TerminalEncodingUTF8 || len(input) == 0 {
		return append([]byte(nil), input...), nil
	}
	if !utf8.Valid(input) {
		return nil, errors.New("终端输入不是有效的 UTF-8 文本")
	}
	codec, err := sftpTextCodec(encodingName)
	if err != nil {
		return nil, fmt.Errorf("读取终端编码 %s 失败：%w", encodingName, err)
	}
	encoded, _, err := transform.Bytes(codec.NewEncoder(), input)
	if err != nil {
		return nil, fmt.Errorf("输入无法用 %s 编码：%w", encodingName, err)
	}
	decoded, _, err := transform.Bytes(codec.NewDecoder(), encoded)
	if err != nil {
		return nil, fmt.Errorf("检查 %s 编码结果失败：%w", encodingName, err)
	}
	if string(decoded) != string(input) {
		return nil, fmt.Errorf("输入包含 %s 无法表示的字符", encodingName)
	}
	return encoded, nil
}

func decodeTerminalOutput(pane *terminalPane, input []byte, atEOF bool) ([]byte, error) {
	encodingName := model.TerminalEncodingUTF8
	if pane != nil {
		encodingName = normalizedTerminalEncoding(pane.terminalEncoding)
	}
	if encodingName == model.TerminalEncodingUTF8 {
		return append([]byte(nil), input...), nil
	}
	if pane == nil {
		return append([]byte(nil), input...), nil
	}
	combined := append(append([]byte(nil), pane.terminalDecodePending...), input...)
	pane.terminalDecodePending = nil
	if len(combined) == 0 {
		return nil, nil
	}
	codec, err := sftpTextCodec(encodingName)
	if err != nil {
		return nil, fmt.Errorf("读取终端编码 %s 失败：%w", encodingName, err)
	}
	decoder := codec.NewDecoder()
	output := make([]byte, max(64, len(combined)*4+16))
	nDst, nSrc, transformErr := decoder.Transform(output, combined, atEOF)
	output = output[:nDst]
	switch transformErr {
	case nil:
		if nSrc != len(combined) {
			return output, fmt.Errorf("终端 %s 解码器只处理了部分输入", encodingName)
		}
	case transform.ErrShortSrc:
		if !atEOF {
			pane.terminalDecodePending = append([]byte(nil), combined[nSrc:]...)
		} else {
			output = append(output, []byte("\uFFFD")...)
		}
	case transform.ErrShortDst:
		return output, errors.New("终端输出解码缓冲区不足")
	default:
		return output, fmt.Errorf("终端输出按 %s 解码失败：%w", encodingName, transformErr)
	}
	return output, nil
}

func nextTerminalEncoding(current string) string {
	options := model.TerminalEncodingOptions()
	current = normalizedTerminalEncoding(current)
	for index, name := range options {
		if name == current {
			return options[(index+1)%len(options)]
		}
	}
	return model.TerminalEncodingUTF8
}

type terminalEncodingCatalog interface {
	SetHostTerminalEncoding(context.Context, string, string) error
}

func (m *modelState) cycleTerminalEncoding(tabID, paneID string) tea.Cmd {
	tab := m.tabByID(tabID)
	if tab == nil {
		return nil
	}
	pane := findPane(tab.root, paneID)
	if pane == nil {
		return nil
	}
	if pane.terminalEncodingSaving {
		m.statusText = "终端编码正在保存，请稍候"
		m.statusErr = false
		return nil
	}
	previous := normalizedTerminalEncoding(pane.terminalEncoding)
	previousInput := normalizedTerminalEncoding(pane.terminalInputEncoding)
	next := nextTerminalEncoding(previous)
	hostID := pane.host.Id
	for _, candidateTab := range m.tabs {
		for _, candidate := range collectPanes(candidateTab.root) {
			if candidate.id == paneID || hostID != "" && candidate.host.Id == hostID {
				candidate.terminalEncoding = next
				candidate.terminalInputEncoding = next
				candidate.terminalDecodePending = nil
				candidate.terminalEncodingSaving = hostID != ""
				candidate.host.TerminalEncoding = next
				candidate.host.TerminalInputEncoding = next
			}
		}
	}
	if hostID == "" {
		pane.statusText = "终端编码已切换为 " + next + "（仅本次连接）"
		pane.statusErr = false
		m.statusText = pane.statusText
		m.statusErr = false
		return nil
	}
	if m.catalog == nil {
		m.restoreTerminalEncoding(hostID, paneID, previous, previousInput)
		m.statusText = "当前没有可用的会话目录，终端编码未保存"
		m.statusErr = true
		return nil
	}
	m.statusText = "正在保存终端编码 " + next + "…"
	m.statusErr = false
	catalog := m.catalog
	updatedHost, hasSavedHost := model.Host{}, false
	if saved := m.hostByID(hostID); saved != nil {
		updatedHost = *saved
		updatedHost.TerminalEncoding = next
		updatedHost.TerminalInputEncoding = next
		hasSavedHost = true
	}
	return func() tea.Msg {
		var err error
		if store, ok := catalog.(terminalEncodingCatalog); ok {
			err = store.SetHostTerminalEncoding(context.Background(), hostID, next)
		} else if hasSavedHost {
			err = catalog.UpdateHost(context.Background(), updatedHost)
		} else {
			err = errors.New("主机已不在会话目录中")
		}
		return terminalEncodingSavedMsg{tabID: tabID, paneID: paneID, hostID: hostID, previous: previous, previousInput: previousInput, encoding: next, err: err}
	}
}

func (m *modelState) handleTerminalEncodingSaved(msg terminalEncodingSavedMsg) {
	tab := m.tabByID(msg.tabID)
	var pane *terminalPane
	if tab != nil {
		pane = findPane(tab.root, msg.paneID)
	}
	if msg.err != nil {
		m.restoreTerminalEncoding(msg.hostID, msg.paneID, msg.previous, msg.previousInput)
		if pane != nil {
			pane.statusText = "保存终端编码失败：" + msg.err.Error()
			pane.statusErr = true
		}
		m.statusText = "保存终端编码失败：" + msg.err.Error()
		m.statusErr = true
		return
	}
	for _, candidateTab := range m.tabs {
		for _, candidate := range collectPanes(candidateTab.root) {
			if candidate.host.Id == msg.hostID {
				candidate.terminalEncodingSaving = false
				candidate.host.TerminalEncoding = msg.encoding
				candidate.terminalInputEncoding = msg.encoding
				candidate.host.TerminalInputEncoding = msg.encoding
			}
		}
	}
	if host := m.hostByID(msg.hostID); host != nil {
		host.TerminalEncoding = msg.encoding
		host.TerminalInputEncoding = msg.encoding
	}
	if pane != nil {
		pane.statusText = "终端编码已保存为 " + msg.encoding
		pane.statusErr = false
	}
	m.statusText = "终端编码已保存为 " + msg.encoding
	m.statusErr = false
}

func (m *modelState) restoreTerminalEncoding(hostID, paneID, encoding, inputEncoding string) {
	encoding = normalizedTerminalEncoding(encoding)
	inputEncoding = normalizedTerminalEncoding(inputEncoding)
	for _, tab := range m.tabs {
		for _, pane := range collectPanes(tab.root) {
			if pane.id == paneID || hostID != "" && pane.host.Id == hostID {
				pane.terminalEncoding = encoding
				pane.terminalInputEncoding = inputEncoding
				pane.terminalDecodePending = nil
				pane.terminalEncodingSaving = false
				pane.host.TerminalEncoding = encoding
				pane.host.TerminalInputEncoding = inputEncoding
			}
		}
	}
}
