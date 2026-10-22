package ftpclient

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode"

	ftp "github.com/jlaffaye/ftp"
)

var rawFTPCommandsManagedByClient = map[string]struct{}{
	"ABOR": {}, "ACCT": {}, "AUTH": {}, "CCC": {}, "CONF": {}, "ENC": {},
	"EPRT": {}, "EPSV": {}, "LIST": {}, "MIC": {}, "MLSD": {}, "MODE": {},
	"NLST": {}, "OPTS": {}, "PASS": {}, "PASV": {}, "PBSZ": {}, "PORT": {}, "PROT": {},
	"QUIT": {}, "REIN": {}, "REST": {}, "RETR": {}, "SMNT": {}, "STOR": {}, "STOU": {},
	"STRU": {}, "TYPE": {}, "USER": {}, "APPE": {},
}

// ValidateRawCommand normalizes one FTP command line and rejects commands
// that would conflict with the client's managed transfer or login state.
func ValidateRawCommand(command string) (string, error) {
	for _, character := range command {
		if unicode.IsControl(character) {
			return "", errors.New("FTP 命令只能包含单行可打印文本")
		}
	}
	command = strings.TrimSpace(command)
	if command == "" {
		return "", errors.New("FTP 命令不能为空")
	}
	if len(command) > 512 {
		return "", errors.New("FTP 命令不能超过 512 字节")
	}
	fields := strings.Fields(command)
	if len(fields) == 0 {
		return "", errors.New("FTP 命令不能为空")
	}
	verb := strings.ToUpper(fields[0])
	for index, character := range verb {
		if character < 'A' || character > 'Z' {
			if index == 0 || character < '0' || character > '9' {
				return "", errors.New("FTP 命令名称必须以英文字母开头")
			}
		}
	}
	if _, managed := rawFTPCommandsManagedByClient[verb]; managed {
		return "", fmt.Errorf("%s 由工作台管理，不能通过原始命令发送", verb)
	}
	return command, nil
}

// RawCommand sends a single non-transfer FTP command through the serialized
// control connection and returns its reply code and text. Server reply codes
// in the 4xx/5xx range are returned as replies, not transport errors.
func (c *Client) RawCommand(ctx context.Context, command string) (int, string, error) {
	command, err := ValidateRawCommand(command)
	if err != nil {
		return 0, "", err
	}
	var code int
	var response string
	err = c.do(ctx, func(conn *ftp.ServerConn) error {
		var commandErr error
		code, response, commandErr = conn.RawCommand(command)
		return commandErr
	})
	if err != nil {
		return code, response, fmt.Errorf("执行 FTP 命令失败：%w", err)
	}
	return code, response, nil
}
