#!/usr/bin/env bash
set -euo pipefail

for tool in go python3 ssh-add ssh-agent ssh-keygen; do
	if ! command -v "$tool" >/dev/null 2>&1; then
		printf '缺少依赖：%s\n' "$tool" >&2
		exit 1
	fi
done

sshd_bin="$(command -v sshd || true)"
if [[ -z "$sshd_bin" ]]; then
	printf '缺少依赖：sshd（OpenSSH server）\n' >&2
	exit 1
fi
if [[ "$(id -u)" -eq 0 ]]; then
	printf '请使用普通用户运行此脚本，sshd 会拒绝 root 登录。\n' >&2
	exit 1
fi

repo_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
tmp_dir="$(mktemp -d "${TMPDIR:-/tmp}/tui-ssh-scp.XXXXXX")"
agent_socket="/tmp/tui-sw-agent-$$.sock"
sshd_pid=""

cleanup() {
	status=$?
	if [[ -n "$sshd_pid" ]] && kill -0 "$sshd_pid" 2>/dev/null; then
		kill "$sshd_pid" 2>/dev/null || true
		wait "$sshd_pid" 2>/dev/null || true
	fi
	if [[ -n "${SSH_AGENT_PID:-}" ]]; then
		ssh-agent -k >/dev/null 2>&1 || true
	fi
	if [[ "$status" -ne 0 && -f "$tmp_dir/sshd.log" ]]; then
		printf '\n--- 临时 sshd 日志 ---\n' >&2
		tail -80 "$tmp_dir/sshd.log" >&2
	fi
	python3 -c 'import pathlib, shutil, sys; shutil.rmtree(pathlib.Path(sys.argv[1]), ignore_errors=True)' "$tmp_dir"
	python3 -c 'import pathlib, sys; pathlib.Path(sys.argv[1]).unlink(missing_ok=True)' "$agent_socket"
	exit "$status"
}
trap cleanup EXIT

mkdir -p "$tmp_dir/home"
chmod 700 "$tmp_dir" "$tmp_dir/home"
ssh-keygen -q -t ed25519 -N '' -f "$tmp_dir/host_key"
ssh-keygen -q -t ed25519 -N '' -f "$tmp_dir/client_key"
cp "$tmp_dir/client_key.pub" "$tmp_dir/authorized_keys"
chmod 600 "$tmp_dir/host_key" "$tmp_dir/client_key" "$tmp_dir/authorized_keys"

username="$(id -un)"
ssh-keygen -q -t ed25519 -N '' -f "$tmp_dir/user_ca"
chmod 600 "$tmp_dir/user_ca" "$tmp_dir/user_ca.pub"
port="$(python3 - <<'PY'
import socket
with socket.socket() as sock:
    sock.bind(("127.0.0.1", 0))
    print(sock.getsockname()[1])
PY
)"
cat >"$tmp_dir/sshd_config" <<EOF
Port $port
ListenAddress 127.0.0.1
HostKey $tmp_dir/host_key
PidFile $tmp_dir/sshd.pid
AuthorizedKeysFile $tmp_dir/authorized_keys
StrictModes no
PermitRootLogin no
PubkeyAuthentication yes
PasswordAuthentication no
KbdInteractiveAuthentication no
UsePAM no
AllowTcpForwarding no
PermitTTY no
AllowUsers $username
TrustedUserCAKeys $tmp_dir/user_ca.pub
Subsystem sftp internal-sftp
LogLevel VERBOSE
EOF
"$sshd_bin" -t -f "$tmp_dir/sshd_config"

eval "$(ssh-agent -s -a "$agent_socket")" >/dev/null
ssh-add "$tmp_dir/client_key" >/dev/null
# Sign after adding the raw key so the agent-only run exercises ordinary
# public-key auth; ssh-add would also load a neighboring certificate file.
ssh-keygen -q -s "$tmp_dir/user_ca" -I "workbench-test-$username" -n "$username" -V '-5m:+5m' "$tmp_dir/client_key.pub"
chmod 600 "$tmp_dir/client_key-cert.pub"
"$sshd_bin" -D -e -f "$tmp_dir/sshd_config" >"$tmp_dir/sshd.log" 2>&1 &
sshd_pid=$!
ready=0
for _ in {1..100}; do
	if ! kill -0 "$sshd_pid" 2>/dev/null; then
		printf '临时 sshd 启动失败\n' >&2
		exit 1
	fi
	if grep -q 'Server listening' "$tmp_dir/sshd.log"; then
		ready=1
		break
	fi
	sleep 0.05
done
if [[ "$ready" -ne 1 ]]; then
	printf '临时 sshd 未在五秒内开始监听\n' >&2
	exit 1
fi

cd "$repo_root"
HOME="$tmp_dir/home" \
XDG_CONFIG_HOME="$tmp_dir/home/.config" \
TUI_SCP_OPENSSH_PORT="$port" \
TUI_SCP_OPENSSH_USER="$username" \
TUI_SCP_OPENSSH_HOST_KEY="$tmp_dir/host_key" \
	go test -count=1 -run '^Test(SCPWorkspace|SFTPWorkspace)AgainstOpenSSH$' ./internal/infra/sshclient

HOME="$tmp_dir/home" \
XDG_CONFIG_HOME="$tmp_dir/home/.config" \
SSH_AUTH_SOCK="" \
TUI_SCP_OPENSSH_PORT="$port" \
TUI_SCP_OPENSSH_USER="$username" \
TUI_SCP_OPENSSH_HOST_KEY="$tmp_dir/host_key" \
TUI_SCP_OPENSSH_CERT_IDENTITY="$tmp_dir/client_key" \
	go test -count=1 -run '^TestSFTPWorkspaceWithOpenSSHUserCertificate$' ./internal/infra/sshclient
