# Workbench patch

This local fork is based on `go.bug.st/serial` v1.8.0 and retains its upstream
BSD license. It adds `Mode.FlowControl` with hardware RTS/CTS and software
XON/XOFF settings on the existing Unix and Windows drivers. Upstream currently
does not expose flow control in `Mode`; the local fork keeps all application
imports on the upstream module path so the remaining driver API stays
compatible.
