# Workbench patch

This local fork adds FTP active-mode data connections (EPRT with PORT fallback
for IPv4) while preserving upstream passive mode. It also exposes `ListUnix` for
servers where Unix rwx permissions are needed, parses permission bits from Unix
`LIST` output and RFC 3659 `UNIX.mode`, and provides a command-injection-checked
`SITE CHMOD` helper. `SITE CHMOD` is a common server extension, not a portable FTP
command, so clients must report server rejection as an expected capability limit.
The fork is based on `github.com/jlaffaye/ftp` v0.2.4 and retains the upstream
license.
