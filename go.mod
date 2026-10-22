module tui-ssh-workbench

go 1.25.0

require (
	charm.land/bubbletea/v2 v2.0.9
	charm.land/lipgloss/v2 v2.0.6
	github.com/BurntSushi/toml v1.5.0
	github.com/UserExistsError/conpty v0.1.4
	github.com/alecthomas/chroma/v2 v2.27.0
	github.com/charmbracelet/ultraviolet v0.0.0-20260811164956-006e29f97886
	github.com/charmbracelet/x/ansi v0.11.8
	github.com/charmbracelet/x/vt v0.1.0
	github.com/creack/pty v1.1.24
	github.com/gen2brain/beeep v0.11.3
	github.com/gogf/gf/v2 v2.10.3
	github.com/jlaffaye/ftp v0.2.4
	github.com/pkg/sftp v1.13.9
	github.com/robfig/cron/v3 v3.0.1
	github.com/zalando/go-keyring v0.2.8
	go.bug.st/serial v1.8.0
	golang.org/x/crypto v0.41.0
	golang.org/x/sys v0.47.0
	golang.org/x/text v0.28.0
	modernc.org/sqlite v1.36.0
)

// Local fork preserves SFTP attribute presence so UID/GID zero is not
// confused with an attribute omitted by the server.
replace github.com/pkg/sftp => ./third_party/pkg-sftp

// Local fork makes emulator response reads and shutdown safe while a pane is active.
replace github.com/charmbracelet/x/vt => ./third_party/vt

// Local fork adds cross-platform serial XON/XOFF and RTS/CTS settings, which
// the upstream serial.Mode does not currently expose.
replace go.bug.st/serial => ./third_party/go-serial

// Local fork adds active FTP data connections for Xftp-compatible transfers.
replace github.com/jlaffaye/ftp => ./third_party/go-ftp

require (
	git.sr.ht/~jackmordaunt/go-toast v1.1.2 // indirect
	github.com/charmbracelet/colorprofile v0.4.3 // indirect
	github.com/charmbracelet/x/exp/ordered v0.1.0 // indirect
	github.com/charmbracelet/x/term v0.2.2 // indirect
	github.com/charmbracelet/x/termios v0.1.1 // indirect
	github.com/charmbracelet/x/windows v0.2.2 // indirect
	github.com/clbanning/mxj/v2 v2.7.0 // indirect
	github.com/clipperhouse/displaywidth v0.11.0 // indirect
	github.com/clipperhouse/uax29/v2 v2.7.0 // indirect
	github.com/danieljoos/wincred v1.2.3 // indirect
	github.com/dlclark/regexp2/v2 v2.2.1 // indirect
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/emirpasic/gods/v2 v2.0.0-alpha // indirect
	github.com/esiqveland/notify v0.13.3 // indirect
	github.com/fatih/color v1.18.0 // indirect
	github.com/fsnotify/fsnotify v1.9.0 // indirect
	github.com/go-logr/logr v1.4.3 // indirect
	github.com/go-logr/stdr v1.2.2 // indirect
	github.com/go-ole/go-ole v1.3.0 // indirect
	github.com/godbus/dbus/v5 v5.2.2 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/jackmordaunt/icns/v3 v3.0.1 // indirect
	github.com/kr/fs v0.1.0 // indirect
	github.com/lucasb-eyer/go-colorful v1.4.1 // indirect
	github.com/magiconair/properties v1.8.10 // indirect
	github.com/mattn/go-colorable v0.1.13 // indirect
	github.com/mattn/go-isatty v0.0.20 // indirect
	github.com/mattn/go-runewidth v0.0.27 // indirect
	github.com/muesli/cancelreader v0.2.2 // indirect
	github.com/ncruces/go-strftime v0.1.9 // indirect
	github.com/nfnt/resize v0.0.0-20180221191011-83c6a9932646 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	github.com/rivo/uniseg v0.4.7 // indirect
	github.com/sergeymakinen/go-bmp v1.0.0 // indirect
	github.com/sergeymakinen/go-ico v1.0.0-beta.0 // indirect
	github.com/tadvi/systray v0.0.0-20190226123456-11a2b8fa57af // indirect
	github.com/xo/terminfo v0.0.0-20220910002029-abceb7e1c41e // indirect
	go.opentelemetry.io/auto/sdk v1.1.0 // indirect
	go.opentelemetry.io/otel v1.38.0 // indirect
	go.opentelemetry.io/otel/metric v1.38.0 // indirect
	go.opentelemetry.io/otel/sdk v1.38.0 // indirect
	go.opentelemetry.io/otel/trace v1.38.0 // indirect
	golang.org/x/exp v0.0.0-20231006140011-7918f672742d // indirect
	golang.org/x/sync v0.22.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
	modernc.org/libc v1.61.13 // indirect
	modernc.org/mathutil v1.7.1 // indirect
	modernc.org/memory v1.8.2 // indirect
)
