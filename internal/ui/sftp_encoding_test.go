package ui

import (
	"os"
	"path/filepath"
	"testing"

	tea "charm.land/bubbletea/v2"
	"tui-ssh-workbench/internal/infra/sftpclient"
)

func TestSFTPTextEncodingsRoundTrip(t *testing.T) {
	cases := []struct {
		name string
		text string
	}{
		{name: "UTF-8", text: "Hello 中文\r\n"},
		{name: "UTF-8 BOM", text: "Hello 中文\r\n"},
		{name: "UTF-16 LE", text: "Hello 中文\r\n"},
		{name: "UTF-16 BE", text: "Hello 中文\r\n"},
		{name: "UTF-32 LE", text: "Hello 中文\r\n"},
		{name: "UTF-32 BE", text: "Hello 中文\r\n"},
		{name: "GB18030", text: "你好，世界\r\n"},
		{name: "GBK", text: "简体中文 GBK\r\n"},
		{name: "Big5", text: "繁體中文\r\n"},
		{name: "Shift_JIS", text: "日本語\r\n"},
		{name: "Windows-1252", text: "caf\u00e9\r\n"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			encoded, err := encodeSFTPText(testCase.text, testCase.name)
			if err != nil {
				t.Fatalf("encode: %v", err)
			}
			decoded, err := decodeSFTPTextWithEncoding(encoded, testCase.name)
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			if decoded != testCase.text {
				t.Fatalf("round trip = %q, want %q", decoded, testCase.text)
			}
		})
	}
}

func TestSFTPTextEncodingAutoDetectsBOMAndGB18030(t *testing.T) {
	for _, testCase := range []struct {
		name string
		text string
	}{
		{name: "UTF-8 BOM", text: "bom 中文"},
		{name: "UTF-16 LE", text: "little endian 中文"},
		{name: "UTF-16 BE", text: "big endian 中文"},
		{name: "UTF-32 LE", text: "UTF-32 little endian"},
		{name: "UTF-32 BE", text: "UTF-32 big endian"},
		{name: "GB18030", text: "编码识别测试"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			encoded, err := encodeSFTPText(testCase.text, testCase.name)
			if err != nil {
				t.Fatalf("encode: %v", err)
			}
			decoded, detected, err := decodeSFTPText(encoded)
			if err != nil {
				t.Fatalf("auto decode: %v", err)
			}
			if decoded != testCase.text || detected != testCase.name {
				t.Fatalf("auto decode = %q (%s), want %q (%s)", decoded, detected, testCase.text, testCase.name)
			}
		})
	}
}

func TestSFTPTextEncodingAutoDetectsBOMlessUnicode(t *testing.T) {
	for _, testCase := range []struct {
		name string
		text string
		bom  int
	}{
		{name: "UTF-16 LE", text: "ASCII legacy file\r\n", bom: 2},
		{name: "UTF-16 BE", text: "ASCII legacy file\r\n", bom: 2},
		{name: "UTF-32 LE", text: "ASCII UTF32 file\r\n", bom: 4},
		{name: "UTF-32 BE", text: "ASCII UTF32 file\r\n", bom: 4},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			encoded, err := encodeSFTPText(testCase.text, testCase.name)
			if err != nil {
				t.Fatalf("encode: %v", err)
			}
			decoded, detected, err := decodeSFTPText(encoded[testCase.bom:])
			if err != nil {
				t.Fatalf("auto decode: %v", err)
			}
			if decoded != testCase.text || detected != testCase.name {
				t.Fatalf("auto decode = %q (%s), want %q (%s)", decoded, detected, testCase.text, testCase.name)
			}
		})
	}
}

func TestSFTPTextEncodingRejectsInvalidUTF32(t *testing.T) {
	for _, raw := range [][]byte{
		{0xFF, 0xFE, 0x00, 0x00, 0x41, 0x00},
		{0xFF, 0xFE, 0x00, 0x00, 0x00, 0xD8, 0x00, 0x00},
	} {
		if _, _, err := decodeSFTPText(raw); err == nil {
			t.Fatalf("invalid UTF-32 bytes %x were accepted", raw)
		}
	}
}

func TestSFTPTextEditorPreservesEncodingOnSave(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.txt")
	original := "第一行\r\n第二行\n"
	raw, err := encodeSFTPText(original, "UTF-16 LE")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o640); err != nil {
		t.Fatal(err)
	}
	rawFromDisk, version, err := readLocalTextFile(path, sftpclient.MaxEditableFileSize)
	if err != nil {
		t.Fatal(err)
	}
	decoded, encodingName, err := decodeSFTPText(rawFromDisk)
	if err != nil {
		t.Fatal(err)
	}
	lines, endings := splitSFTPText(decoded)
	state := modelState{height: 16, sftp: &sftpBrowserState{
		standalone: true,
		generation: 3,
		activeSide: sftpLocalSide,
		editor: &sftpEditorState{
			mode: sftpEditMode, side: sftpLocalSide, path: path,
			encoding: encodingName, originalEncoding: encodingName,
			bom: true, originalBOM: true, rawContent: raw, version: version,
			original: decoded, lines: lines, lineEndings: endings,
			newline: preferredSFTPNewline(endings),
		},
	}}
	state.sftp.editor.lines[0] = "修改后的第一行"
	state.sftp.editor.dirty = true
	command := state.saveSFTPTextEditor()
	if command == nil {
		t.Fatal("save command was not created")
	}
	saved, ok := command().(sftpEditorSavedMsg)
	if !ok {
		t.Fatal("save command returned an unexpected message")
	}
	state.handleSFTPTextSaved(saved)
	gotRaw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	wantRaw, err := encodeSFTPText("修改后的第一行\r\n第二行\n", "UTF-16 LE")
	if err != nil {
		t.Fatal(err)
	}
	if string(gotRaw) != string(wantRaw) {
		t.Fatalf("saved bytes do not preserve UTF-16 LE encoding\n got: %x\nwant: %x", gotRaw, wantRaw)
	}
	if state.sftp.editor.encoding != "UTF-16 LE" {
		t.Fatalf("saved editor encoding = %q, want UTF-16 LE", state.sftp.editor.encoding)
	}
}

func TestSFTPTextEditorPreservesBOMlessUnicodeOnSave(t *testing.T) {
	for _, testCase := range []struct {
		name string
		text string
		bom  int
	}{
		{name: "UTF-16 LE", text: "ASCII legacy text\r\n", bom: 2},
		{name: "UTF-16 BE", text: "ASCII legacy text\r\n", bom: 2},
		{name: "UTF-32 LE", text: "ASCII UTF32 text\r\n", bom: 4},
		{name: "UTF-32 BE", text: "ASCII UTF32 text\r\n", bom: 4},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			filePath := filepath.Join(t.TempDir(), "legacy.txt")
			encodedWithBOM, err := encodeSFTPText(testCase.text, testCase.name)
			if err != nil {
				t.Fatal(err)
			}
			raw := append([]byte(nil), encodedWithBOM[testCase.bom:]...)
			if err := os.WriteFile(filePath, raw, 0o640); err != nil {
				t.Fatal(err)
			}
			decoded, detected, err := decodeSFTPText(raw)
			if err != nil {
				t.Fatalf("decode no-BOM file: %v", err)
			}
			if detected != testCase.name || decoded != testCase.text {
				t.Fatalf("detected %s %q, want %s %q", detected, decoded, testCase.name, testCase.text)
			}
			lines, endings := splitSFTPText(decoded)
			content, version, err := readLocalTextFile(filePath, sftpclient.MaxEditableFileSize)
			if err != nil {
				t.Fatal(err)
			}
			state := modelState{height: 14, sftp: &sftpBrowserState{
				standalone: true, generation: 2, activeSide: sftpLocalSide,
				editor: &sftpEditorState{
					mode: sftpEditMode, side: sftpLocalSide, path: filePath,
					encoding: detected, originalEncoding: detected,
					bom: false, originalBOM: false, rawContent: content, version: version,
					original: decoded, lines: lines, lineEndings: endings,
					newline: preferredSFTPNewline(endings),
				},
			}}
			state.sftp.editor.lines[0] = "updated text"
			state.sftp.editor.dirty = true
			command := state.saveSFTPTextEditor()
			if command == nil {
				t.Fatal("save command was not created")
			}
			saved, ok := command().(sftpEditorSavedMsg)
			if !ok {
				t.Fatal("save returned an unexpected message")
			}
			state.handleSFTPTextSaved(saved)
			got, err := os.ReadFile(filePath)
			if err != nil {
				t.Fatal(err)
			}
			want, err := encodeSFTPTextWithBOM("updated text\r\n", testCase.name, false)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != string(want) {
				t.Fatalf("save changed the no-BOM encoding marker\n got: %x\nwant: %x", got, want)
			}
		})
	}
}

func TestSFTPTextEditorCanChangeEncodingWithoutLosingDirtyText(t *testing.T) {
	original := "中文"
	raw, err := encodeSFTPText(original, "GB18030")
	if err != nil {
		t.Fatal(err)
	}
	lines, endings := splitSFTPText(original)
	editor := &sftpEditorState{
		mode:     sftpEditMode,
		encoding: "GB18030", rawContent: raw,
		original: original, lines: lines, lineEndings: endings, dirty: true,
	}
	editor.lines[0] += " 😀"
	wantText := editor.lines[0]
	if err := cycleSFTPTextEncoding(editor); err != nil {
		t.Fatalf("cycle encoding with unrepresentable edit: %v", err)
	}
	if editor.encoding != "UTF-8" {
		t.Fatalf("encoding = %q, want UTF-8 for the new emoji", editor.encoding)
	}
	if got := serializeSFTPText(editor.lines, editor.lineEndings); got != wantText {
		t.Fatalf("changing encoding lost the edit: got %q, want %q", got, wantText)
	}
	if !editor.dirty {
		t.Fatal("changing the save encoding cleared the dirty state")
	}
}

func TestSFTPTextEditorCtrlUCyclesAndSavesEncodingConversion(t *testing.T) {
	filePath := filepath.Join(t.TempDir(), "plain.txt")
	if err := os.WriteFile(filePath, []byte("plain text"), 0o600); err != nil {
		t.Fatal(err)
	}
	raw, version, err := readLocalTextFile(filePath, sftpclient.MaxEditableFileSize)
	if err != nil {
		t.Fatal(err)
	}
	text, encodingName, err := decodeSFTPText(raw)
	if err != nil {
		t.Fatal(err)
	}
	lines, endings := splitSFTPText(text)
	state := modelState{height: 12, sftp: &sftpBrowserState{
		standalone: true,
		generation: 7,
		activeSide: sftpLocalSide,
		editor: &sftpEditorState{
			mode: sftpEditMode, side: sftpLocalSide, path: filePath,
			encoding: encodingName, originalEncoding: encodingName,
			rawContent: raw, version: version, original: text,
			lines: lines, lineEndings: endings,
		},
	}}
	updated, _ := state.updateSFTPTextEditor(tea.Key{Code: 'u', Mod: tea.ModCtrl})
	state = unwrapModel(t, updated)
	if state.sftp.editor.encoding != "UTF-8 BOM" || !state.sftp.editor.dirty {
		t.Fatalf("Ctrl+U state = encoding %q, dirty %v; want UTF-8 BOM conversion", state.sftp.editor.encoding, state.sftp.editor.dirty)
	}
	command := state.saveSFTPTextEditor()
	if command == nil {
		t.Fatal("saving an encoding-only change did not start")
	}
	saved, ok := command().(sftpEditorSavedMsg)
	if !ok {
		t.Fatal("save returned an unexpected message")
	}
	state.handleSFTPTextSaved(saved)
	got, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatal(err)
	}
	want := append([]byte{0xEF, 0xBB, 0xBF}, []byte("plain text")...)
	if string(got) != string(want) {
		t.Fatalf("encoding conversion wrote %x, want %x", got, want)
	}
}
