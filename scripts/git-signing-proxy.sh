#!/usr/bin/env bash
#
# Git signing wrapper that delegates to the git-signing-proxy service.
# Works as both gpg.ssh.program (SSH signing) and gpg.program (GPG signing).
#
# Environment:
#   SIGNING_PROXY_SOCKET - Unix socket path (auto-detected at /tmp/claude/git-signing-proxy.sock)
#   SIGNING_PROXY_URL    - Base URL for TCP mode (default: http://git-signing-proxy:8080)
#
# SSH mode setup:
#   git config gpg.format ssh
#   git config gpg.ssh.program /path/to/git-signing-proxy.sh
#   git config user.signingKey <key-id>   # must match a filename in /etc/signing-keys/
#   git config commit.gpgSign true
#
# GPG mode setup:
#   git config gpg.format openpgp
#   git config gpg.program /path/to/git-signing-proxy.sh
#   git config user.signingKey <key-id>
#   git config commit.gpgSign true

set -euo pipefail

SIGNING_PROXY_SOCKET="${SIGNING_PROXY_SOCKET:-}"
SIGNING_PROXY_URL="${SIGNING_PROXY_URL:-http://git-signing-proxy:8080}"

# Auto-detect: prefer Unix socket if it exists
if [[ -z "$SIGNING_PROXY_SOCKET" && -S "/tmp/claude/git-signing-proxy.sock" ]]; then
    SIGNING_PROXY_SOCKET="/tmp/claude/git-signing-proxy.sock"
fi

if [[ -n "$SIGNING_PROXY_SOCKET" ]]; then
    CURL_SOCKET_ARGS=(--unix-socket "${SIGNING_PROXY_SOCKET}")
    CURL_BASE_URL="http://localhost"
else
    CURL_SOCKET_ARGS=()
    CURL_BASE_URL="${SIGNING_PROXY_URL}"
fi

die() { echo "git-signing-proxy: $*" >&2; exit 1; }

# --- SSH mode ---
if [[ "${1:-}" == "-Y" ]]; then
    # Pass non-sign operations (verify, find-principals, check-novalidate)
    # through to the local ssh-keygen for signature verification.
    if [[ "${2:-}" != "sign" ]]; then
        exec ssh-keygen "$@"
    fi
    shift 2
    key_id=""
    buffer_file=""

    while [[ $# -gt 0 ]]; do
        case "$1" in
            -n) shift 2 ;;
            -f) key_id=$(basename "$2"); shift 2 ;;
            *)  buffer_file="$1"; shift ;;
        esac
    done

    [[ -n "$key_id" ]]     || die "no key specified (-f)"
    [[ -n "$buffer_file" ]] || die "no buffer file specified"
    [[ -f "$buffer_file" ]] || die "buffer file not found: $buffer_file"

    sig=$(curl -sf --max-time 30 \
        "${CURL_SOCKET_ARGS[@]}" \
        --data-binary "@${buffer_file}" \
        "${CURL_BASE_URL}/sign/${key_id}") \
        || die "POST /sign/${key_id} failed"

    printf '%s\n' "$sig" > "${buffer_file}.sig"
    exit 0
fi

# --- GPG mode: called as gpg --status-fd=2 -bsau <key-id> ---
status_fd=""
key_id=""

while [[ $# -gt 0 ]]; do
    case "$1" in
        --status-fd=*) status_fd="${1#--status-fd=}"; shift ;;
        --status-fd)   status_fd="$2"; shift 2 ;;
        -u)            key_id="$2"; shift 2 ;;
        -*u)           key_id="$2"; shift 2 ;;
        -*)            shift ;;
        *)             shift ;;
    esac
done

[[ -n "$key_id" ]] || die "no signing key specified"

sig=$(curl -sf --max-time 30 \
    "${CURL_SOCKET_ARGS[@]}" \
    --data-binary @- \
    "${CURL_BASE_URL}/sign/${key_id}") \
    || die "POST /sign/${key_id} failed"

if [[ -n "$status_fd" ]]; then
    printf '[GNUPG:] SIG_CREATED D\n' >&"${status_fd}"
fi

printf '%s\n' "$sig"
