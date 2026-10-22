#!/usr/bin/env python3
"""Start an isolated pyftpdlib FTP or FTPS interoperability server."""

import argparse
import logging
from pathlib import Path

from pyftpdlib.authorizers import DummyAuthorizer
from pyftpdlib.handlers import FTPHandler, TLS_FTPHandler
from pyftpdlib.servers import FTPServer


class RESTTraceMixin:
    rest_trace = ""

    def ftp_REST(self, line):
        with open(self.rest_trace, "a", encoding="ascii") as trace:
            trace.write(str(line).strip() + "\n")
        return super().ftp_REST(line)


class FXPTraceMixin:
    fxp_trace = ""
    reject_port = False

    def _trace_fxp(self, command):
        if self.fxp_trace:
            with open(self.fxp_trace, "a", encoding="ascii") as trace:
                trace.write(command + "\n")

    def ftp_PORT(self, line):
        self._trace_fxp("PORT")
        if self.reject_port:
            self._trace_fxp("PORT_REJECTED")
            self.respond("501 FXP active data connections are disabled.")
            return
        self._trace_fxp("PORT_ACCEPTED")
        return super().ftp_PORT(line)

    def ftp_EPSV(self, line):
        self._trace_fxp("EPSV")
        return super().ftp_EPSV(line)

    def ftp_PASV(self, line):
        self._trace_fxp("PASV")
        return super().ftp_PASV(line)

    def ftp_RETR(self, line):
        self._trace_fxp("RETR")
        return super().ftp_RETR(line)


class PlainHandler(FXPTraceMixin, RESTTraceMixin, FTPHandler):
    pass


class ExplicitTLSHandler(RESTTraceMixin, TLS_FTPHandler):
    pass


class ImplicitTLSHandler(ExplicitTLSHandler):
    def handle(self):
        self.secure_connection(self.ssl_context)

    def handle_ssl_established(self):
        TLS_FTPHandler.handle(self)

    def ftp_AUTH(self, arg):
        self.respond("503 AUTH is not used with implicit FTPS.")


def main():
    logging.basicConfig(level=logging.DEBUG)
    parser = argparse.ArgumentParser()
    parser.add_argument("--mode", choices=("off", "explicit", "implicit"), required=True)
    parser.add_argument("--port", type=int, required=True)
    parser.add_argument("--root", required=True)
    parser.add_argument("--rest-trace", required=True)
    parser.add_argument("--fxp-trace", default="")
    parser.add_argument("--permit-fxp", action="store_true")
    parser.add_argument("--reject-port", action="store_true")
    parser.add_argument("--ready", required=True)
    parser.add_argument("--certificate")
    parser.add_argument("--private-key")
    args = parser.parse_args()

    root = Path(args.root).resolve()
    root.mkdir(parents=True, exist_ok=True)
    (root / ".interop-hidden").write_text("hidden", encoding="ascii")
    authorizer = DummyAuthorizer()
    authorizer.add_user("workbench", "interop-test-password", str(root), perm="elradfmwMT")

    if args.mode in ("explicit", "implicit"):
        if not args.certificate or not args.private_key:
            parser.error("FTPS requires --certificate and --private-key")
        handler = ExplicitTLSHandler if args.mode == "explicit" else ImplicitTLSHandler
        handler.certfile = args.certificate
        handler.keyfile = args.private_key
        handler.tls_control_required = True
        handler.tls_data_required = True
    else:
        handler = PlainHandler

    handler.authorizer = authorizer
    handler.rest_trace = args.rest_trace
    handler.fxp_trace = args.fxp_trace
    handler.reject_port = args.reject_port
    handler.permit_foreign_addresses = args.permit_fxp
    handler.masquerade_address = "127.0.0.1"

    server = FTPServer(("127.0.0.1", args.port), handler)
    server.max_cons = 4
    server.max_cons_per_ip = 4
    Path(args.ready).write_text("ready\n", encoding="ascii")
    server.serve_forever(timeout=0.2, blocking=True, handle_exit=True)


if __name__ == "__main__":
    main()
