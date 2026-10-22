package terminalog

import (
	"errors"
	"fmt"
	"regexp"
	"regexp/syntax"
	"sort"
	"unicode/utf8"
)

const (
	maxRedactionWidth = 4096
	maxRedactionRules = 32
	maxPatternBytes   = 1024
	redactionMarker   = "[REDACTED]"
)

type textMatch struct {
	start int
	end   int
}

// Redactor applies bounded regular expressions to printable terminal output.
// It keeps enough trailing bytes to avoid leaking a match split across writes.
type Redactor struct {
	patterns []*regexp.Regexp
	maxWidth int
	pending  []byte
}

// NewRedactor compiles bounded regular expressions. Unbounded repetition and
// context-sensitive anchors are rejected so chunked output can be redacted
// without buffering an unlimited amount of terminal data.
func NewRedactor(patterns []string) (*Redactor, error) {
	if len(patterns) > maxRedactionRules {
		return nil, fmt.Errorf("terminal log redaction supports at most %d patterns", maxRedactionRules)
	}
	redactor := &Redactor{}
	for _, pattern := range patterns {
		if pattern == "" {
			return nil, errors.New("terminal log redaction pattern is empty")
		}
		if len(pattern) > maxPatternBytes {
			return nil, fmt.Errorf("terminal log redaction pattern exceeds %d bytes", maxPatternBytes)
		}
		compiled, err := regexp.Compile(pattern)
		if err != nil {
			return nil, fmt.Errorf("compile terminal log redaction pattern %q: %w", pattern, err)
		}
		if compiled.MatchString("") {
			return nil, fmt.Errorf("terminal log redaction pattern %q must not match empty text", pattern)
		}
		parsed, err := syntax.Parse(pattern, syntax.Perl)
		if err != nil {
			return nil, fmt.Errorf("parse terminal log redaction pattern %q: %w", pattern, err)
		}
		width, err := maxRedactionMatchWidth(parsed)
		if err != nil {
			return nil, fmt.Errorf("terminal log redaction pattern %q: %w", pattern, err)
		}
		if width <= 0 || width > maxRedactionWidth {
			return nil, fmt.Errorf("terminal log redaction pattern %q has an unsupported match width", pattern)
		}
		redactor.patterns = append(redactor.patterns, compiled)
		if width > redactor.maxWidth {
			redactor.maxWidth = width
		}
	}
	return redactor, nil
}

// ValidatePatterns validates a configured redaction rule set.
func ValidatePatterns(patterns []string) error {
	_, err := NewRedactor(patterns)
	return err
}

// Write returns the redacted prefix that is safe to persist now. A bounded
// suffix remains buffered until enough lookahead arrives or Flush is called.
func (r *Redactor) Write(data []byte) []byte {
	if r == nil || len(r.patterns) == 0 {
		return data
	}
	buffer := make([]byte, 0, len(r.pending)+len(data))
	buffer = append(buffer, r.pending...)
	buffer = append(buffer, data...)
	safe := len(buffer) - r.maxWidth
	for safe > 0 && safe < len(buffer) && buffer[safe]&0xC0 == 0x80 {
		safe--
	}
	if safe <= 0 {
		r.pending = buffer
		return nil
	}
	output, consumed := r.redactPrefix(buffer, safe)
	r.pending = append(r.pending[:0], buffer[consumed:]...)
	return output
}

// Flush redacts and returns all buffered output. It must be called once when
// the recording is stopped.
func (r *Redactor) Flush() []byte {
	if r == nil || len(r.pending) == 0 {
		return nil
	}
	if len(r.patterns) == 0 {
		output := r.pending
		r.pending = nil
		return output
	}
	output, _ := r.redactPrefix(r.pending, len(r.pending))
	r.pending = nil
	return output
}

func (r *Redactor) redactPrefix(buffer []byte, safe int) ([]byte, int) {
	matches := r.findMatches(buffer)
	cut := safe
	for _, match := range matches {
		if match.start < cut && match.end > cut {
			cut = match.end
		}
	}
	var output []byte
	position := 0
	for _, match := range matches {
		if match.start >= cut || match.end > cut {
			break
		}
		output = append(output, buffer[position:match.start]...)
		output = append(output, redactionMarker...)
		position = match.end
	}
	output = append(output, buffer[position:cut]...)
	return output, cut
}

func (r *Redactor) findMatches(buffer []byte) []textMatch {
	matches := make([]textMatch, 0)
	for _, pattern := range r.patterns {
		for _, bounds := range pattern.FindAllIndex(buffer, -1) {
			if bounds[0] == bounds[1] || !printableMatch(buffer[bounds[0]:bounds[1]]) {
				continue
			}
			matches = append(matches, textMatch{start: bounds[0], end: bounds[1]})
		}
	}
	sort.Slice(matches, func(i, j int) bool {
		if matches[i].start == matches[j].start {
			return matches[i].end > matches[j].end
		}
		return matches[i].start < matches[j].start
	})
	selected := matches[:0]
	for _, match := range matches {
		if len(selected) == 0 || match.start > selected[len(selected)-1].end {
			selected = append(selected, match)
		} else if match.end > selected[len(selected)-1].end {
			selected[len(selected)-1].end = match.end
		}
	}
	return selected
}

func printableMatch(data []byte) bool {
	for _, value := range data {
		if value < 0x20 || value == 0x7f {
			return false
		}
	}
	return true
}

func maxRedactionMatchWidth(expression *syntax.Regexp) (int, error) {
	switch expression.Op {
	case syntax.OpNoMatch, syntax.OpEmptyMatch:
		return 0, nil
	case syntax.OpBeginLine, syntax.OpEndLine, syntax.OpBeginText, syntax.OpEndText,
		syntax.OpWordBoundary, syntax.OpNoWordBoundary:
		return 0, errors.New("line/text anchors and word boundaries are not supported")
	case syntax.OpLiteral:
		width := 0
		for _, character := range expression.Rune {
			runeWidth := utf8.RuneLen(character)
			if expression.Flags&syntax.FoldCase != 0 {
				runeWidth = 4
			}
			width += runeWidth
		}
		return width, nil
	case syntax.OpCharClass, syntax.OpAnyCharNotNL, syntax.OpAnyChar:
		return 4, nil
	case syntax.OpCapture, syntax.OpQuest:
		return maxRedactionMatchWidth(expression.Sub[0])
	case syntax.OpRepeat:
		if expression.Max < 0 {
			return 0, errors.New("unbounded repetition is not supported; use a finite {min,max} range")
		}
		childWidth, err := maxRedactionMatchWidth(expression.Sub[0])
		if err != nil {
			return 0, err
		}
		return childWidth * expression.Max, nil
	case syntax.OpConcat:
		width := 0
		for _, child := range expression.Sub {
			childWidth, err := maxRedactionMatchWidth(child)
			if err != nil {
				return 0, err
			}
			width += childWidth
		}
		return width, nil
	case syntax.OpAlternate:
		width := 0
		for _, child := range expression.Sub {
			childWidth, err := maxRedactionMatchWidth(child)
			if err != nil {
				return 0, err
			}
			if childWidth > width {
				width = childWidth
			}
		}
		return width, nil
	case syntax.OpStar, syntax.OpPlus:
		return 0, errors.New("unbounded repetition is not supported; use a finite {min,max} range")
	default:
		return 0, fmt.Errorf("regular expression operation %s is not supported", expression.Op)
	}
}
