# git-signing-proxy

A lightweight HTTP service that signs Git commits on behalf of isolated sandbox
environments. It holds private SSH or GPG keys and exposes a single signing
endpoint, allowing sandboxed agents to produce signed commits without ever
having direct access to the keys.

```
 +-------------------+          POST /sign/{key-id}         +---------------------+
 |  Sandbox          | ----- payload (HTTP) --------------> |  git-signing-proxy  |
 |  (git commit)     | <---- SSH/GPG signature ------------ |  (keys in memory)   |
 +-------------------+                                      +---------------------+
```

## Features

- **SSH** (SSHSIG) and **GPG** (OpenPGP) signatures, with SSH as the default
- Pure Go signing — no runtime dependency on `ssh-keygen` or `gpg`
- Content-based key detection — point `KEYS_DIR` at `~/.ssh` directly
- Unix socket and TCP listening modes
- Distroless container image, non-root, read-only filesystem
- OpenShift restricted-v2 SCC compatible

## Quick Start (Local)

The proxy detects private keys by scanning file content for `PRIVATE KEY`
headers, so `~/.ssh` works directly as the keys directory — no need to copy
keys elsewhere.

```bash
# Run as a bare binary (Unix socket mode, keys from ~/.ssh)
make run

# Or run in a container (TCP on 127.0.0.1:8080, keys from ~/.ssh)
make run-local

# Test (TCP mode)
echo "hello" | curl -sf --data-binary @- http://127.0.0.1:8080/sign/id_ed25519
```

When using TCP mode, the proxy binds to `127.0.0.1` by default to avoid
exposing the signing endpoint to the network. Override with
`LISTEN_ADDR=0.0.0.0:8080` only if you understand the implications.

## Git Configuration

Configure Git to use the wrapper script as the signing program:

```bash
git config --global gpg.format ssh
git config --global gpg.ssh.program /path/to/scripts/git-signing-proxy.sh
git config --global user.signingKey id_ed25519    # filename in KEYS_DIR
git config --global commit.gpgSign true
```

The wrapper auto-detects the connection method:

1. If a Unix socket exists at the default path, uses it
2. Otherwise falls back to `SIGNING_PROXY_URL` (default `http://git-signing-proxy:8080`)

Override with environment variables:

```bash
export SIGNING_PROXY_SOCKET=/path/to/socket    # force Unix socket
export SIGNING_PROXY_URL=http://host:8080      # force TCP
```

Verification operations (`-Y verify`, `-Y find-principals`) are passed through
to the local `ssh-keygen`. For GPG keys, set `gpg.format openpgp` and
`gpg.program` instead of `gpg.ssh.program`.

### Claude Code Sandbox Integration

The Unix socket defaults to `/tmp/claude/git-signing-proxy.sock` because
Claude Code's bubblewrap sandbox grants read/write access to `/tmp/claude/`.
The sandbox's network namespace blocks `localhost` access, making the Unix
socket the preferred transport. If your sandbox blocks Unix sockets (seccomp),
add a DNS alias (e.g., `signing.host`) pointing to `127.0.0.1` in
`/etc/hosts` and add it to the sandbox's `allowedDomains` — this bypasses the
`NO_PROXY` rules that block private IP ranges.

For non-Claude use cases, set `LISTEN_SOCKET` to a more conventional path
such as `$XDG_RUNTIME_DIR/git-signing-proxy.sock`.

## Deploying to OpenShift

> **Warning:** The signing key secret contains your private key in plain text
> (base64-encoded, not encrypted). Ensure the namespace has strict RBAC
> policies — anyone with `get`/`list` access to secrets in the namespace can
> read your private key. Do not deploy to shared or untrusted clusters without
> reviewing who has secret access.

```bash
# Build and push
make docker-build docker-push IMG=quay.io/youruser/git-signing-proxy:latest

# Create the signing key secret
make create-secret SECRET_KEY_FILE=~/.ssh/id_ed25519 SECRET_KEY_ID=id_ed25519

# Deploy
make deploy IMG=quay.io/youruser/git-signing-proxy:latest

# Undeploy
make undeploy
```

To add multiple keys to the same secret:

```bash
oc create secret generic git-signing-keys \
  --from-file=my-ssh-key=~/.ssh/id_ed25519 \
  --from-file=my-gpg-key=~/keys/gpg-private.asc \
  --dry-run=client -o yaml | oc apply -f -
```

### Network Isolation

Create a NetworkPolicy to restrict access to the signing proxy. Only pods
that need to sign commits (e.g., sandbox workloads) should be able to reach
port 8080:

```yaml
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: git-signing-proxy
spec:
  podSelector:
    matchLabels:
      app.kubernetes.io/name: git-signing-proxy
  policyTypes:
    - Ingress
  ingress:
    - from:
        - podSelector:
            matchLabels:
              role: sandbox
      ports:
        - port: 8080
          protocol: TCP
```

Adjust the `role: sandbox` label selector to match your sandbox pods.

## API

**`POST /sign/{keyID}`** — Signs the request body and returns the armored
signature. `keyID` matches a filename in the keys directory. Max payload 10 MB.
Returns `400` for invalid input, `404` for unknown key, `413` for oversized
payload, `500` on failure.

**`GET /healthz`** — Returns `200` if at least one key is loaded, `503`
otherwise.

## Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `KEYS_DIR` | `/etc/signing-keys` | Directory containing private key files (`~/.ssh` in the Makefile) |
| `LISTEN_ADDR` | `:8080` | TCP listen address (Makefile binds to `127.0.0.1:8080`) |
| `LISTEN_SOCKET` | *(unset)* | Unix socket path; disables TCP when set |

## Security

- Private keys never leave the proxy — the sandbox only receives signatures
- Key IDs validated against `^[a-zA-Z0-9][a-zA-Z0-9._-]*$`
- Non-root container with read-only filesystem, all capabilities dropped
- Pure Go crypto — no shell, no shell-outs
- Unix sockets restricted to owner access (mode 0600)
- Request bodies capped at 10 MB; oversized payloads rejected
- TCP mode binds to `127.0.0.1` by default
- **OpenShift:** use a NetworkPolicy to restrict which pods can reach the proxy
- **OpenShift:** review namespace RBAC before creating the signing key secret

## License

Apache License 2.0. See [LICENSE](LICENSE) for details.
