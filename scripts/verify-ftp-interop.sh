#!/usr/bin/env bash
set -euo pipefail

for tool in go python3 openssl; do
	if ! command -v "$tool" >/dev/null 2>&1; then
		printf '缺少依赖：%s\n' "$tool" >&2
		exit 1
	fi
done

repo_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
tmp_dir="$(mktemp -d "${TMPDIR:-/tmp}/tui-ftp-interop.XXXXXX")"
server_pids=()

stop_server() {
	for server_pid in "${server_pids[@]:-}"; do
		[[ -n "$server_pid" ]] || continue
		if kill -0 "$server_pid" 2>/dev/null; then
			kill "$server_pid" 2>/dev/null || true
			wait "$server_pid" 2>/dev/null || true
		fi
	done
	server_pids=()
}

allocate_port() {
	python3 - <<'PY'
import socket
with socket.socket() as sock:
    sock.bind(("127.0.0.1", 0))
    print(sock.getsockname()[1])
PY
}

start_server() {
	local ready="$1"
	local log="$2"
	shift 2
	"$tmp_dir/venv/bin/python" "$repo_root/scripts/ftp-interop-server.py" "$@" >"$log" 2>&1 &
	local server_pid=$!
	server_pids+=("$server_pid")
	for _ in {1..100}; do
		if ! kill -0 "$server_pid" 2>/dev/null; then
			printf '临时 FTP 服务端启动失败（日志：%s）\n' "$log" >&2
			return 1
		fi
		if [[ -f "$ready" ]]; then
			return 0
		fi
		sleep 0.05
	done
	printf '临时 FTP 服务端未在五秒内就绪（日志：%s）\n' "$log" >&2
	return 1
}

cleanup() {
	status=$?
	stop_server
	if [[ "$status" -ne 0 ]]; then
		for log in "$tmp_dir"/*.log; do
			[[ -f "$log" ]] || continue
			printf '\n--- 临时 FTP/FTPS 服务端日志：%s ---\n' "$log" >&2
			tail -100 "$log" >&2
		done
	fi
	python3 -c 'import pathlib, shutil, sys; shutil.rmtree(pathlib.Path(sys.argv[1]), ignore_errors=True)' "$tmp_dir"
	exit "$status"
}
trap cleanup EXIT

python3 -m venv "$tmp_dir/venv"
"$tmp_dir/venv/bin/python" -m pip install --disable-pip-version-check --quiet 'pyftpdlib[ssl]==2.2.0'
openssl req -x509 -newkey rsa:2048 -nodes -sha256 -days 1 \
	-keyout "$tmp_dir/ftps.key" -out "$tmp_dir/ftps.crt" \
	-subj '/CN=localhost' -addext 'subjectAltName=DNS:localhost,IP:127.0.0.1' >/dev/null 2>&1
chmod 600 "$tmp_dir/ftps.key"

for mode in off explicit implicit; do
	for active_mode in passive active; do
		root="$tmp_dir/root-$mode-$active_mode"
		mkdir -p "$root"
		ready="$tmp_dir/ready-$mode-$active_mode"
		rest_trace="$tmp_dir/rest-$mode-$active_mode.log"
		touch "$rest_trace"
		port="$(allocate_port)"
		server_args=(
			"$repo_root/scripts/ftp-interop-server.py"
			--mode "$mode"
			--port "$port"
			--root "$root"
			--rest-trace "$rest_trace"
			--ready "$ready"
		)
		if [[ "$mode" != off ]]; then
			server_args+=(--certificate "$tmp_dir/ftps.crt" --private-key "$tmp_dir/ftps.key")
		fi
		server_log="$tmp_dir/server-$mode-$active_mode.log"
		"$tmp_dir/venv/bin/python" "${server_args[@]}" >"$server_log" 2>&1 &
		server_pid=$!
		server_pids+=("$server_pid")
		started=0
		for _ in {1..100}; do
			if ! kill -0 "$server_pid" 2>/dev/null; then
				printf '临时 %s FTP 服务端启动失败\n' "$mode" >&2
				exit 1
			fi
			if [[ -f "$ready" ]]; then
				started=1
				break
			fi
			sleep 0.05
		done
		if [[ "$started" -ne 1 ]]; then
			printf '临时 %s FTP 服务端未在五秒内就绪\n' "$mode" >&2
			exit 1
		fi

		active_flag=0
		if [[ "$active_mode" == active ]]; then active_flag=1; fi
		cd "$repo_root"
		TUI_FTP_INTEROP_MODE="$mode" \
		TUI_FTP_INTEROP_ACTIVE="$active_flag" \
		TUI_FTP_INTEROP_PORT="$port" \
		TUI_FTP_INTEROP_CERT="$tmp_dir/ftps.crt" \
		TUI_FTP_INTEROP_REST_LOG="$rest_trace" \
		go test -count=1 -run '^TestFTPClientAgainstPyftpdlib$' ./internal/infra/ftpclient
		for expected in '<- LIST -a /' '-> 550 No such file or directory.' '<- MLSD /'; do
			if ! grep -Fq -- "$expected" "$server_log"; then
				printf 'FTP fallback trace is missing %q\n' "$expected" >&2
				exit 1
			fi
		done
		stop_server
	done
done

for fxp_case in direct fallback; do
	source_root="$tmp_dir/fxp-source-$fxp_case"
	destination_root="$tmp_dir/fxp-destination-$fxp_case"
	mkdir -p "$source_root" "$destination_root"
	printf 'real FTP server FXP payload' >"$source_root/fxp-source.txt"
	source_port="$(allocate_port)"
	destination_port="$(allocate_port)"
	source_ready="$tmp_dir/fxp-source-ready-$fxp_case"
	destination_ready="$tmp_dir/fxp-destination-ready-$fxp_case"
	source_trace="$tmp_dir/fxp-source-trace-$fxp_case.log"
	: >"$source_trace"
	source_args=(
		--mode off --port "$source_port" --root "$source_root" \
		--rest-trace "$tmp_dir/fxp-source-rest-$fxp_case.log" --ready "$source_ready" \
		--fxp-trace "$source_trace" --permit-fxp
	)
	if [[ "$fxp_case" == fallback ]]; then
		source_args+=(--reject-port)
	fi
	start_server "$source_ready" "$tmp_dir/fxp-source-server-$fxp_case.log" "${source_args[@]}"
	start_server "$destination_ready" "$tmp_dir/fxp-destination-server-$fxp_case.log" \
		--mode off --port "$destination_port" --root "$destination_root" \
		--rest-trace "$tmp_dir/fxp-destination-rest-$fxp_case.log" --ready "$destination_ready" \
		--permit-fxp
	cd "$repo_root"
	TUI_FTP_FXP_CASE="$fxp_case" \
	TUI_FTP_FXP_SOURCE_PORT="$source_port" \
	TUI_FTP_FXP_DESTINATION_PORT="$destination_port" \
	TUI_FTP_FXP_SOURCE_TRACE="$source_trace" \
	go test -count=1 -run '^TestFTPClientFXPAgainstPyftpdlib$' ./internal/infra/ftpclient
	stop_server
done
