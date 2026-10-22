package desktopnotify

import (
	"errors"
	"strings"
	"unicode"

	"github.com/gen2brain/beeep"
)

const (
	applicationName = "SSH 终端工作台"
	maxTitleRunes   = 64
	maxBodyRunes    = 240
)

// Notifier sends a short notification through the operating system.
type Notifier interface {
	Notify(title, body string) error
}

// SystemNotifier uses the desktop notification service available on the host.
// Notification failures are returned to callers so they can be logged or
// deliberately ignored without affecting the operation being reported.
type SystemNotifier struct{}

func init() {
	beeep.AppName = applicationName
}

func (SystemNotifier) Notify(title, body string) error {
	title = cleanText(title, maxTitleRunes)
	body = cleanText(body, maxBodyRunes)
	if title == "" || body == "" {
		return errors.New("notification title and body must not be empty")
	}
	return beeep.Notify(title, body, nil)
}

func cleanText(value string, maxRunes int) string {
	value = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, value)
	value = strings.Join(strings.Fields(value), " ")
	runes := []rune(value)
	if len(runes) > maxRunes {
		value = string(runes[:maxRunes-1]) + "…"
	}
	return value
}
