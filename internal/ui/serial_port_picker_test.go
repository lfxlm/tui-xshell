package ui

import (
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"tui-ssh-workbench/internal/model"
)

func TestSerialPortPickerLoadsAndAppliesSelectedDevice(t *testing.T) {
	form := newHostForm("")
	form.hostKind, form.field, form.address = model.HostKindSerial, 2, "/dev/ttyUSB0"
	state := modelState{
		form: form,
		serialPortLister: func() ([]string, error) {
			return []string{"/dev/ttyUSB0", "/dev/ttyUSB1"}, nil
		},
	}
	updated, command := state.updateKey(tea.KeyPressMsg(tea.Key{Code: 'p', Mod: tea.ModCtrl}))
	state = unwrapModel(t, updated)
	if command == nil || state.serialPortPicker == nil || !state.serialPortPicker.loading {
		t.Fatal("Ctrl+P did not start asynchronous port discovery")
	}
	result := command()
	message, ok := result.(serialPortsLoadedMsg)
	if !ok {
		t.Fatalf("port discovery returned %T, want serialPortsLoadedMsg", result)
	}
	updated, _ = state.Update(message)
	state = unwrapModel(t, updated)
	if state.serialPortPicker == nil || state.serialPortPicker.loading || state.serialPortPicker.index != 0 {
		t.Fatalf("loaded picker = %#v, want current device preselected", state.serialPortPicker)
	}
	updated, _ = state.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyDown}))
	state = unwrapModel(t, updated)
	updated, _ = state.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	state = unwrapModel(t, updated)
	if state.serialPortPicker != nil || state.form.address != "/dev/ttyUSB1" {
		t.Fatalf("selected device was not returned to form: picker=%#v address=%q", state.serialPortPicker, state.form.address)
	}
}

func TestSerialPortPickerKeepsManualEntryAvailableWhenNoDevicesFound(t *testing.T) {
	form := newHostForm("")
	form.hostKind, form.field, form.address = model.HostKindSerial, 2, "/dev/custom-console"
	state := modelState{form: form, width: 100, height: 30, serialPortLister: func() ([]string, error) {
		return nil, nil
	}}
	updated, command := state.updateKey(tea.KeyPressMsg(tea.Key{Code: 'p', Mod: tea.ModCtrl}))
	state = unwrapModel(t, updated)
	message := command().(serialPortsLoadedMsg)
	updated, _ = state.Update(message)
	state = unwrapModel(t, updated)
	if state.serialPortPicker == nil || !strings.Contains(state.serialPortPicker.errorText, "手动输入") {
		t.Fatalf("empty-device result lost manual-entry guidance: %#v", state.serialPortPicker)
	}
	if view := state.serialPortPickerView(); !strings.Contains(view, "手动输入") {
		t.Fatalf("picker view does not explain manual entry: %s", view)
	}
	updated, _ = state.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
	state = unwrapModel(t, updated)
	if state.serialPortPicker != nil || state.form.address != "/dev/custom-console" {
		t.Fatalf("manual device path changed after closing picker: %#v", state.form)
	}
}

func TestSerialPortPickerIgnoresStaleResultsAndShowsErrors(t *testing.T) {
	form := newHostForm("")
	form.hostKind, form.field = model.HostKindSerial, 2
	listCalls := 0
	state := modelState{form: form, serialPortLister: func() ([]string, error) {
		listCalls++
		if listCalls == 1 {
			return []string{"/dev/old"}, nil
		}
		return nil, errors.New("permission denied")
	}}
	updated, oldCommand := state.updateKey(tea.KeyPressMsg(tea.Key{Code: 'p', Mod: tea.ModCtrl}))
	state = unwrapModel(t, updated)
	oldRequest := state.serialPortPicker.requestID
	updated, _ = state.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
	state = unwrapModel(t, updated)
	updated, newCommand := state.updateKey(tea.KeyPressMsg(tea.Key{Code: 'p', Mod: tea.ModCtrl}))
	state = unwrapModel(t, updated)
	newRequest := state.serialPortPicker.requestID
	if newRequest == oldRequest {
		t.Fatal("reopened picker reused its request id")
	}
	updated, _ = state.Update(oldCommand().(serialPortsLoadedMsg))
	state = unwrapModel(t, updated)
	if state.serialPortPicker.requestID != newRequest || !state.serialPortPicker.loading {
		t.Fatalf("stale result replaced current port scan: %#v", state.serialPortPicker)
	}
	updated, _ = state.Update(newCommand().(serialPortsLoadedMsg))
	state = unwrapModel(t, updated)
	if state.serialPortPicker.loading || !strings.Contains(state.serialPortPicker.errorText, "permission denied") {
		t.Fatalf("port enumeration error was not shown: %#v", state.serialPortPicker)
	}
}
