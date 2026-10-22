package ui

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
)

func refreshSFTPTextHighlight(editor *sftpEditorState) {
	if editor == nil {
		return
	}
	editor.highlightedLines = highlightSFTPText(editor.name, editor.lines)
}

func highlightSFTPText(filename string, lines []string) []string {
	lexer := lexers.Match(filename)
	if lexer == nil || len(lines) == 0 {
		return nil
	}
	safeLines := make([]string, len(lines))
	for index, line := range lines {
		safeLines[index] = sanitizeRemoteText(strings.ReplaceAll(line, "\t", "    "))
	}
	content := strings.Join(safeLines, "\n")
	iterator, err := lexer.Tokenise(&chroma.TokeniseOptions{State: "root", EnsureLF: false}, content)
	if err != nil {
		return nil
	}
	var builders []strings.Builder
	builders = make([]strings.Builder, len(lines))
	lineIndex := 0
	for token := iterator(); token != chroma.EOF; token = iterator() {
		value := token.Value
		style, styled := sftpSyntaxStyle(token.Type)
		for value != "" {
			newline := strings.IndexByte(value, '\n')
			chunk := value
			if newline >= 0 {
				chunk, value = value[:newline], value[newline+1:]
			} else {
				value = ""
			}
			if lineIndex < len(builders) && chunk != "" {
				if styled {
					builders[lineIndex].WriteString(style.Render(chunk))
				} else {
					builders[lineIndex].WriteString(chunk)
				}
			}
			if newline < 0 {
				break
			}
			lineIndex++
		}
	}
	result := make([]string, len(builders))
	for index := range builders {
		result[index] = builders[index].String()
	}
	return result
}

func sftpSyntaxStyle(tokenType chroma.TokenType) (lipgloss.Style, bool) {
	switch {
	case tokenType.InCategory(chroma.Comment):
		return mutedStyle, true
	case tokenType.InCategory(chroma.Keyword), tokenType.InCategory(chroma.NameBuiltin), tokenType.InCategory(chroma.NameConstant):
		return accentStyle, true
	case tokenType.InCategory(chroma.LiteralString):
		return warningStyle, true
	case tokenType.InCategory(chroma.LiteralNumber):
		return lipgloss.NewStyle().Foreground(palette.accent), true
	case tokenType.InCategory(chroma.NameFunction), tokenType.InCategory(chroma.NameClass):
		return titleStyle, true
	case tokenType.InCategory(chroma.NameVariable):
		return secondaryStyle, true
	case tokenType.InCategory(chroma.Operator), tokenType.InCategory(chroma.Punctuation):
		return mutedStyle, true
	default:
		return lipgloss.NewStyle(), false
	}
}
