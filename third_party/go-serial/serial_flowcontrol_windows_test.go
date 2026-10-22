//go:build windows

package serial

import (
	"testing"

	"golang.org/x/sys/windows"
)

func TestWindowsSetModeParamsSetsFlowControl(t *testing.T) {
	port := &windowsPort{}
	for _, test := range []struct {
		name     string
		flow     FlowControl
		wantCTS  bool
		wantRTS  bool
		wantInX  bool
		wantOutX bool
	}{
		{name: "none", flow: NoFlowControl},
		{name: "hardware", flow: HardwareFlowControl, wantCTS: true, wantRTS: true},
		{name: "software", flow: SoftwareFlowControl, wantInX: true, wantOutX: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			params := windows.DCB{}
			if err := port.setModeParams(&Mode{FlowControl: test.flow}, &params); err != nil {
				t.Fatal(err)
			}
			if got := params.Flags&dcbOutXCTSFlow != 0; got != test.wantCTS {
				t.Errorf("CTS flow = %v, want %v", got, test.wantCTS)
			}
			if got := params.Flags&dcbRTSControlHandshake != 0; got != test.wantRTS {
				t.Errorf("RTS handshake = %v, want %v", got, test.wantRTS)
			}
			if got := params.Flags&dcbInX != 0; got != test.wantInX {
				t.Errorf("XON input = %v, want %v", got, test.wantInX)
			}
			if got := params.Flags&dcbOutX != 0; got != test.wantOutX {
				t.Errorf("XON output = %v, want %v", got, test.wantOutX)
			}
		})
	}
	if err := port.setModeParams(&Mode{FlowControl: FlowControl(99)}, &windows.DCB{}); err == nil {
		t.Fatal("unknown flow-control value was accepted")
	}
}
