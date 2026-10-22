//
// Copyright 2014-2026 Cristian Maglie. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.
//

package enumerator

import (
	"os"
	"path/filepath"
	"testing"
)

// TestReadLineEmptyFile checks that readLine treats a zero-byte file (which is
// common for sysfs USB attributes such as "serial", "manufacturer" or
// "product" when the device exposes the attribute but leaves it blank) as an
// empty string, NOT as an error.
//
// Before the fix readLine returned io.EOF for an empty file. That error is
// propagated by parseUSBSysFS and aborts the whole GetDetailedPortsList()
// enumeration, so a single device with an empty (but present) sysfs attribute
// makes the entire port list unavailable.
func TestReadLineEmptyFile(t *testing.T) {
	dir := t.TempDir()

	empty := filepath.Join(dir, "empty")
	if err := os.WriteFile(empty, []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	line, err := readLine(empty)
	if err != nil {
		t.Fatalf("readLine on an empty file returned error %v, want nil", err)
	}
	if line != "" {
		t.Fatalf("readLine on an empty file returned %q, want empty string", line)
	}
}

// TestReadLineMissingFile documents the already-working "attribute absent" case.
func TestReadLineMissingFile(t *testing.T) {
	line, err := readLine(filepath.Join(t.TempDir(), "does-not-exist"))
	if err != nil {
		t.Fatalf("readLine on a missing file returned error %v, want nil", err)
	}
	if line != "" {
		t.Fatalf("readLine on a missing file returned %q, want empty string", line)
	}
}

// TestReadLineContent makes sure normal content (with and without a trailing
// newline) is still read correctly and the trailing newline is stripped.
func TestReadLineContent(t *testing.T) {
	dir := t.TempDir()
	cases := map[string]string{
		"0403\n": "0403",
		"6001":   "6001",
	}
	for content, want := range cases {
		f := filepath.Join(dir, "f")
		if err := os.WriteFile(f, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		got, err := readLine(f)
		if err != nil {
			t.Fatalf("readLine(%q) error: %v", content, err)
		}
		if got != want {
			t.Fatalf("readLine(%q) = %q, want %q", content, got, want)
		}
	}
}

// TestParseUSBSysFSEmptySerial reproduces the real-world failure: a USB device
// directory whose "serial" attribute exists but is empty. parseUSBSysFS must
// succeed and yield an empty SerialNumber, not fail enumeration.
func TestParseUSBSysFSEmptySerial(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("idVendor", "2341\n")
	write("idProduct", "0043\n")
	write("serial", "")       // present but empty
	write("manufacturer", "") // present but empty
	write("product", "Arduino Uno\n")

	details := &PortDetails{}
	if err := parseUSBSysFS(dir, details); err != nil {
		t.Fatalf("parseUSBSysFS returned error %v on a device with an empty serial attribute", err)
	}
	if details.VID != "2341" || details.PID != "0043" {
		t.Fatalf("got VID=%q PID=%q, want 2341/0043", details.VID, details.PID)
	}
	if details.SerialNumber != "" {
		t.Fatalf("SerialNumber = %q, want empty", details.SerialNumber)
	}
	if details.Product != "Arduino Uno" {
		t.Fatalf("Product = %q, want %q", details.Product, "Arduino Uno")
	}
}

// TestParseUSBSysFSVIDPIDUppercase checks that VID/PID read from sysfs (which
// stores them lowercase) are normalized to uppercase, matching the darwin and
// Windows backends, while the serial number's case is preserved.
func TestParseUSBSysFSVIDPIDUppercase(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("idVendor", "10c4\n") // CP2102, lowercase in sysfs
	write("idProduct", "ea60\n")
	write("serial", "abcDEF0123\n") // mixed case must be preserved
	write("manufacturer", "Silicon Labs\n")
	write("product", "CP2102 USB to UART\n")

	details := &PortDetails{}
	if err := parseUSBSysFS(dir, details); err != nil {
		t.Fatalf("parseUSBSysFS returned error %v", err)
	}
	if details.VID != "10C4" || details.PID != "EA60" {
		t.Fatalf("got VID=%q PID=%q, want 10C4/EA60 (uppercase)", details.VID, details.PID)
	}
	if details.SerialNumber != "abcDEF0123" {
		t.Fatalf("SerialNumber = %q, want case preserved", details.SerialNumber)
	}
}
