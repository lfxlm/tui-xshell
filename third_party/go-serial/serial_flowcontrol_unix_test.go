//go:build linux || darwin || freebsd || openbsd

package serial

import (
	"testing"

	"golang.org/x/sys/unix"
)

func TestSetTermSettingsFlowControl(t *testing.T) {
	settings := &unix.Termios{}
	if err := setTermSettingsFlowControl(SoftwareFlowControl, settings); err != nil {
		t.Fatal(err)
	}
	if settings.Iflag&(unix.IXON|unix.IXOFF) != unix.IXON|unix.IXOFF {
		t.Fatalf("software flow flags = %#x, want IXON|IXOFF", settings.Iflag)
	}
	if settings.Iflag&unix.IXANY != 0 || settings.Cflag&tcCRTSCTS != 0 {
		t.Fatalf("software flow enabled unrelated flow flags: iflag=%#x cflag=%#x", settings.Iflag, settings.Cflag)
	}

	settings = &unix.Termios{}
	if err := setTermSettingsFlowControl(NoFlowControl, settings); err != nil {
		t.Fatal(err)
	}
	if settings.Iflag&(unix.IXON|unix.IXOFF|unix.IXANY) != 0 || settings.Cflag&tcCRTSCTS != 0 {
		t.Fatalf("none flow flags = iflag %#x cflag %#x", settings.Iflag, settings.Cflag)
	}

	if tcCRTSCTS != 0 {
		settings = &unix.Termios{}
		if err := setTermSettingsFlowControl(HardwareFlowControl, settings); err != nil {
			t.Fatal(err)
		}
		if settings.Cflag&tcCRTSCTS != tcCRTSCTS || settings.Iflag&(unix.IXON|unix.IXOFF) != 0 {
			t.Fatalf("hardware flow flags = iflag %#x cflag %#x", settings.Iflag, settings.Cflag)
		}
	}
	if err := setTermSettingsFlowControl(FlowControl(99), &unix.Termios{}); err == nil {
		t.Fatal("unknown flow-control value was accepted")
	}
}
