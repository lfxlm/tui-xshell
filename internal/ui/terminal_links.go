package ui

import (
	"errors"
	"net/url"
	"os/exec"
	"regexp"
	"runtime"
	"strings"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
)

var terminalURLPattern = regexp.MustCompile(`(?i)https?://[^\s"'<>]+`)

func (m *modelState) openTerminalLink(raw string) tea.Cmd {
	link, err := normalizeTerminalURL(raw)
	if err != nil {
		m.statusText = "链接地址无效或暂不支持"
		m.statusErr = true
		return nil
	}
	m.statusText = "正在打开链接…"
	m.statusErr = false
	return func() tea.Msg {
		return terminalLinkOpenedMsg{url: link, err: openExternalURL(link)}
	}
}

func normalizeTerminalURL(raw string) (string, error) {
	raw = strings.TrimRight(strings.TrimSpace(raw), ".,;:!?)]}")
	parsed, err := url.Parse(raw)
	if err != nil || parsed == nil {
		return "", errors.New("unsupported URL")
	}
	scheme := strings.ToLower(parsed.Scheme)
	if parsed.Hostname() == "" || (scheme != "http" && scheme != "https") {
		return "", errors.New("unsupported URL")
	}
	parsed.Scheme = scheme
	return parsed.String(), nil
}

func openExternalURL(raw string) error {
	var command *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		command = exec.Command("open", raw)
	case "windows":
		command = exec.Command("rundll32", "url.dll,FileProtocolHandler", raw)
	default:
		command = exec.Command("xdg-open", raw)
	}
	if err := command.Start(); err != nil {
		return err
	}
	go func() { _ = command.Wait() }()
	return nil
}

func terminalLinkAt(pane *terminalPane, row, col int) string {
	if pane == nil || pane.terminal == nil || row < 0 || row >= pane.terminal.Height() || col < 0 || col >= pane.terminal.Width() {
		return ""
	}
	if cell := pane.terminal.CellAt(col, row); cell != nil && cell.Link.URL != "" {
		if link, err := normalizeTerminalURL(cell.Link.URL); err == nil {
			return link
		}
	}
	for _, link := range terminalLinkRanges(snapshotTerminalLine(pane, row)) {
		if col >= link.startCell && col <= link.endCell {
			return link.url
		}
	}
	return ""
}

func renderTerminalRowsWithLinks(pane *terminalPane) []string {
	if pane == nil || pane.terminal == nil {
		return nil
	}
	rows := make([]string, pane.terminal.Height())
	for row := range rows {
		line := snapshotTerminalLine(pane, row)
		for _, link := range terminalLinkRanges(line) {
			for col := link.startCell; col <= link.endCell && col < len(line); col++ {
				if line[col].Link.URL == "" {
					line[col].Link = uv.NewLink(link.url)
				}
			}
		}
		rows[row] = line.Render()
	}
	return rows
}

func snapshotTerminalLine(pane *terminalPane, row int) uv.Line {
	line := make(uv.Line, max(1, pane.terminal.Width()))
	for col := range line {
		if cell := pane.terminal.CellAt(col, row); cell != nil {
			line[col] = *cell
		} else {
			line[col] = uv.EmptyCell
		}
	}
	return line
}

type terminalLinkRange struct {
	startCell int
	endCell   int
	url       string
}

func terminalLinkRanges(line uv.Line) []terminalLinkRange {

	var text strings.Builder
	type cellRange struct {
		startByte, endByte int
		startCell, endCell int
	}
	ranges := make([]cellRange, 0, len(line))
	for x := 0; x < len(line); x++ {
		cell := line[x]
		if cell.Width == 0 {
			continue
		}
		content := cell.Content
		if content == "" {
			content = " "
		}
		start := text.Len()
		text.WriteString(content)
		end := text.Len()
		if cell.Link.URL == "" {
			ranges = append(ranges, cellRange{
				startByte: start, endByte: end,
				startCell: x, endCell: x + max(1, cell.Width) - 1,
			})
		}
		x += max(0, int(cell.Width)-1)
	}
	rowText := text.String()
	links := make([]terminalLinkRange, 0)
	for _, match := range terminalURLPattern.FindAllStringIndex(rowText, -1) {
		candidate := strings.TrimRight(rowText[match[0]:match[1]], ".,;:!?)]}")
		if candidate == "" {
			continue
		}
		matchEnd := match[0] + len(candidate)
		startCell, endCell := -1, -1
		for _, cells := range ranges {
			if cells.startByte >= matchEnd || cells.endByte <= match[0] {
				continue
			}
			if startCell < 0 || cells.startCell < startCell {
				startCell = cells.startCell
			}
			if cells.endCell > endCell {
				endCell = cells.endCell
			}
		}
		link, err := normalizeTerminalURL(candidate)
		if err == nil && startCell >= 0 {
			links = append(links, terminalLinkRange{startCell: startCell, endCell: endCell, url: link})
		}
	}
	return links
}
