package ui

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/encoding/japanese"
	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/encoding/traditionalchinese"
	textunicode "golang.org/x/text/encoding/unicode"
	"golang.org/x/text/transform"
)

var sftpTextEncodingNames = []string{
	"UTF-8", "UTF-8 BOM", "UTF-16 LE", "UTF-16 BE", "UTF-32 LE", "UTF-32 BE",
	"GB18030", "GBK", "Big5", "Shift_JIS", "Windows-1252",
}

func decodeSFTPText(content []byte) (string, string, error) {
	switch {
	case bytes.HasPrefix(content, []byte{0xFF, 0xFE, 0x00, 0x00}):
		text, err := decodeSFTPTextWithEncoding(content, "UTF-32 LE")
		return text, "UTF-32 LE", err
	case bytes.HasPrefix(content, []byte{0x00, 0x00, 0xFE, 0xFF}):
		text, err := decodeSFTPTextWithEncoding(content, "UTF-32 BE")
		return text, "UTF-32 BE", err
	case bytes.HasPrefix(content, []byte{0xEF, 0xBB, 0xBF}):
		text, err := decodeSFTPTextWithEncoding(content, "UTF-8 BOM")
		return text, "UTF-8 BOM", err
	case bytes.HasPrefix(content, []byte{0xFF, 0xFE}):
		text, err := decodeSFTPTextWithEncoding(content, "UTF-16 LE")
		return text, "UTF-16 LE", err
	case bytes.HasPrefix(content, []byte{0xFE, 0xFF}):
		text, err := decodeSFTPTextWithEncoding(content, "UTF-16 BE")
		return text, "UTF-16 BE", err
	}
	if name, ok := detectSFTPBOMlessUnicode(content); ok {
		text, err := decodeSFTPTextWithEncoding(content, name)
		if err == nil {
			return text, name, nil
		}
	}
	if utf8.Valid(content) {
		text, err := decodeSFTPTextWithEncoding(content, "UTF-8")
		return text, "UTF-8", err
	}

	// UTF-8 without a BOM is unambiguous. For legacy files, prefer common
	// East Asian encodings and retain a universal single-byte fallback; the
	// editor lets the user cycle encodings before editing or saving.
	for _, name := range []string{"GB18030", "GBK", "Big5", "Shift_JIS", "Windows-1252"} {
		text, err := decodeSFTPTextWithEncoding(content, name)
		if err == nil {
			return text, name, nil
		}
	}
	return "", "", errors.New("无法识别文本编码或文件包含二进制内容")
}

func decodeSFTPTextWithEncoding(content []byte, name string) (string, error) {
	var decoded []byte
	switch name {
	case "UTF-8":
		if !utf8.Valid(content) {
			return "", errors.New("内容不是有效的 UTF-8")
		}
		decoded = content
	case "UTF-8 BOM":
		content = bytes.TrimPrefix(content, []byte{0xEF, 0xBB, 0xBF})
		if !utf8.Valid(content) {
			return "", errors.New("内容不是有效的 UTF-8")
		}
		decoded = content
	case "UTF-16 LE", "UTF-16 BE":
		endian := textunicode.LittleEndian
		if name == "UTF-16 BE" {
			endian = textunicode.BigEndian
		}
		if bytes.HasPrefix(content, []byte{0xFF, 0xFE}) {
			if endian != textunicode.LittleEndian {
				return "", errors.New("文件 BOM 与所选编码不一致")
			}
			content = content[2:]
		} else if bytes.HasPrefix(content, []byte{0xFE, 0xFF}) {
			if endian != textunicode.BigEndian {
				return "", errors.New("文件 BOM 与所选编码不一致")
			}
			content = content[2:]
		}
		var err error
		decoded, _, err = transform.Bytes(textunicode.UTF16(endian, textunicode.IgnoreBOM).NewDecoder(), content)
		if err != nil {
			return "", err
		}
	case "UTF-32 LE", "UTF-32 BE":
		littleEndian := name == "UTF-32 LE"
		if bytes.HasPrefix(content, []byte{0xFF, 0xFE, 0x00, 0x00}) {
			if !littleEndian {
				return "", errors.New("文件 BOM 与所选编码不一致")
			}
			content = content[4:]
		} else if bytes.HasPrefix(content, []byte{0x00, 0x00, 0xFE, 0xFF}) {
			if littleEndian {
				return "", errors.New("文件 BOM 与所选编码不一致")
			}
			content = content[4:]
		}
		var err error
		decoded, err = decodeSFTPUTF32(content, littleEndian)
		if err != nil {
			return "", err
		}
	default:
		codec, err := sftpTextCodec(name)
		if err != nil {
			return "", err
		}
		decoded, _, err = transform.Bytes(codec.NewDecoder(), content)
		if err != nil {
			return "", err
		}
	}
	text := string(decoded)
	if !utf8.ValidString(text) || !isLikelySFTPText(text) {
		return "", errors.New("内容包含二进制或不可显示字符")
	}
	return text, nil
}

func encodeSFTPText(text string, name string) ([]byte, error) {
	return encodeSFTPTextWithBOM(text, name, sftpTextEncodingHasDefaultBOM(name))
}

func encodeSFTPTextWithBOM(text string, name string, bom bool) ([]byte, error) {
	if !utf8.ValidString(text) {
		return nil, errors.New("编辑内容不是有效的 UTF-8")
	}
	switch name {
	case "UTF-8":
		return []byte(text), nil
	case "UTF-8 BOM":
		if bom {
			return append([]byte{0xEF, 0xBB, 0xBF}, []byte(text)...), nil
		}
		return []byte(text), nil
	case "UTF-16 LE", "UTF-16 BE":
		endian := textunicode.LittleEndian
		bomBytes := []byte{0xFF, 0xFE}
		if name == "UTF-16 BE" {
			endian = textunicode.BigEndian
			bomBytes = []byte{0xFE, 0xFF}
		}
		encoded, _, err := transform.Bytes(textunicode.UTF16(endian, textunicode.IgnoreBOM).NewEncoder(), []byte(text))
		if err != nil {
			return nil, err
		}
		if bom {
			return append(bomBytes, encoded...), nil
		}
		return encoded, nil
	case "UTF-32 LE", "UTF-32 BE":
		littleEndian := name == "UTF-32 LE"
		bomBytes := []byte{0xFF, 0xFE, 0x00, 0x00}
		if !littleEndian {
			bomBytes = []byte{0x00, 0x00, 0xFE, 0xFF}
		}
		encoded := encodeSFTPUTF32(text, littleEndian)
		if bom {
			return append(bomBytes, encoded...), nil
		}
		return encoded, nil
	default:
		codec, err := sftpTextCodec(name)
		if err != nil {
			return nil, err
		}
		encoded, _, err := transform.Bytes(codec.NewEncoder(), []byte(text))
		if err != nil {
			return nil, fmt.Errorf("无法用 %s 编码：%w", name, err)
		}
		return encoded, nil
	}
}

func sftpTextEncodingHasDefaultBOM(name string) bool {
	switch name {
	case "UTF-8 BOM", "UTF-16 LE", "UTF-16 BE", "UTF-32 LE", "UTF-32 BE":
		return true
	default:
		return false
	}
}

func hasSFTPTextBOM(content []byte, name string) bool {
	switch name {
	case "UTF-8 BOM":
		return bytes.HasPrefix(content, []byte{0xEF, 0xBB, 0xBF})
	case "UTF-16 LE":
		return bytes.HasPrefix(content, []byte{0xFF, 0xFE})
	case "UTF-16 BE":
		return bytes.HasPrefix(content, []byte{0xFE, 0xFF})
	case "UTF-32 LE":
		return bytes.HasPrefix(content, []byte{0xFF, 0xFE, 0x00, 0x00})
	case "UTF-32 BE":
		return bytes.HasPrefix(content, []byte{0x00, 0x00, 0xFE, 0xFF})
	default:
		return false
	}
}

func decodeSFTPUTF32(content []byte, littleEndian bool) ([]byte, error) {
	if len(content)%4 != 0 {
		return nil, errors.New("UTF-32 内容长度不是 4 字节的倍数")
	}
	decoded := make([]byte, 0, len(content))
	for index := 0; index < len(content); index += 4 {
		var value uint32
		if littleEndian {
			value = uint32(content[index]) | uint32(content[index+1])<<8 | uint32(content[index+2])<<16 | uint32(content[index+3])<<24
		} else {
			value = uint32(content[index])<<24 | uint32(content[index+1])<<16 | uint32(content[index+2])<<8 | uint32(content[index+3])
		}
		if value > utf8.MaxRune || value >= 0xD800 && value <= 0xDFFF {
			return nil, errors.New("UTF-32 内容包含无效 Unicode 码点")
		}
		decoded = utf8.AppendRune(decoded, rune(value))
	}
	return decoded, nil
}

func encodeSFTPUTF32(text string, littleEndian bool) []byte {
	encoded := make([]byte, 0, len(text)*4)
	for _, character := range text {
		value := uint32(character)
		if littleEndian {
			encoded = append(encoded, byte(value), byte(value>>8), byte(value>>16), byte(value>>24))
		} else {
			encoded = append(encoded, byte(value>>24), byte(value>>16), byte(value>>8), byte(value))
		}
	}
	return encoded
}

func detectSFTPBOMlessUnicode(content []byte) (string, bool) {
	if len(content) < 4 {
		return "", false
	}
	if len(content)%4 == 0 && len(content) >= 8 {
		units := len(content) / 4
		checked := min(units, 64)
		var nonzeroFirst, zeroSecond, zeroThird, zeroFourth int
		var zeroFirst, zeroSecondBE, zeroThirdBE, nonzeroFourth int
		for unit := 0; unit < checked; unit++ {
			index := unit * 4
			if content[index] != 0 {
				nonzeroFirst++
			}
			if content[index+1] == 0 {
				zeroSecond++
			}
			if content[index+2] == 0 {
				zeroThird++
			}
			if content[index+3] == 0 {
				zeroFourth++
			}
			if content[index] == 0 {
				zeroFirst++
			}
			if content[index+1] == 0 {
				zeroSecondBE++
			}
			if content[index+2] == 0 {
				zeroThirdBE++
			}
			if content[index+3] != 0 {
				nonzeroFourth++
			}
		}
		if nonzeroFirst*4 >= checked*3 && zeroSecond*4 >= checked*3 && zeroThird*4 >= checked*3 && zeroFourth*4 >= checked*3 {
			return "UTF-32 LE", true
		}
		if zeroFirst*4 >= checked*3 && zeroSecondBE*4 >= checked*3 && zeroThirdBE*4 >= checked*3 && nonzeroFourth*4 >= checked*3 {
			return "UTF-32 BE", true
		}
	}
	if len(content)%2 != 0 || len(content) < 4 {
		return "", false
	}
	units := len(content) / 2
	checked := min(units, 128)
	var zeroEven, nonzeroEven, zeroOdd, nonzeroOdd int
	for unit := 0; unit < checked; unit++ {
		even, odd := content[unit*2], content[unit*2+1]
		if even == 0 {
			zeroEven++
		} else {
			nonzeroEven++
		}
		if odd == 0 {
			zeroOdd++
		} else {
			nonzeroOdd++
		}
	}
	if nonzeroEven*4 >= checked*3 && zeroOdd*4 >= checked*3 {
		return "UTF-16 LE", true
	}
	if zeroEven*4 >= checked*3 && nonzeroOdd*4 >= checked*3 {
		return "UTF-16 BE", true
	}
	return "", false
}

func sftpTextCodec(name string) (encoding.Encoding, error) {
	switch name {
	case "GBK":
		return simplifiedchinese.GBK, nil
	case "GB18030":
		return simplifiedchinese.GB18030, nil
	case "Big5":
		return traditionalchinese.Big5, nil
	case "Shift_JIS":
		return japanese.ShiftJIS, nil
	case "Windows-1252":
		return charmap.Windows1252, nil
	default:
		return nil, fmt.Errorf("不支持的文本编码：%s", name)
	}
}

func isLikelySFTPText(text string) bool {
	if !utf8.ValidString(text) || strings.IndexByte(text, 0) >= 0 {
		return false
	}
	runeCount, unusualCount := 0, 0
	for _, character := range text {
		runeCount++
		if unicode.IsControl(character) && character != '\n' && character != '\r' && character != '\t' && character != '\f' {
			unusualCount++
		}
		if character == utf8.RuneError {
			unusualCount++
		}
	}
	return unusualCount == 0 || unusualCount*100 <= runeCount
}

func cycleSFTPTextEncoding(editor *sftpEditorState) error {
	if editor == nil {
		return errors.New("编辑器不可用")
	}
	current := 0
	for index, name := range sftpTextEncodingNames {
		if name == editor.encoding {
			current = index
			break
		}
	}
	if editor.originalEncoding == "" {
		editor.originalEncoding = editor.encoding
	}
	if editor.mode == sftpEditMode {
		return cycleSFTPTextSaveEncoding(editor, current)
	}
	var lastErr error
	for offset := 1; offset <= len(sftpTextEncodingNames); offset++ {
		name := sftpTextEncodingNames[(current+offset)%len(sftpTextEncodingNames)]
		if name == editor.encoding {
			continue
		}
		text, err := decodeSFTPTextWithEncoding(editor.rawContent, name)
		if err != nil {
			lastErr = err
			continue
		}
		editor.encoding = name
		editor.bom = hasSFTPTextBOM(editor.rawContent, name)
		editor.original = text
		editor.lines, editor.lineEndings = splitSFTPText(text)
		editor.newline = preferredSFTPNewline(editor.lineEndings)
		editor.row, editor.column, editor.topLine = 0, 0, 0
		editor.dirty = false
		return nil
	}
	if lastErr == nil {
		lastErr = errors.New("没有可用的文本编码")
	}
	return lastErr
}

func cycleSFTPTextSaveEncoding(editor *sftpEditorState, current int) error {
	content := serializeSFTPText(editor.lines, editor.lineEndings)
	for offset := 1; offset <= len(sftpTextEncodingNames); offset++ {
		name := sftpTextEncodingNames[(current+offset)%len(sftpTextEncodingNames)]
		if name == editor.encoding {
			continue
		}
		bom := sftpTextEncodingHasDefaultBOM(name)
		if _, err := encodeSFTPTextWithBOM(content, name, bom); err == nil {
			editor.encoding = name
			editor.bom = bom
			editor.dirty = content != editor.original || editor.encoding != editor.originalEncoding || editor.bom != editor.originalBOM
			return nil
		}
	}
	return errors.New("当前修改无法使用支持的文本编码保存")
}
