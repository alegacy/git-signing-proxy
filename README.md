# git-signing-proxy

A lightweight HTTP service that signs Git commits on behalf of isolated sandbox
environments. It holds private SSH or GPG keys and exposes a single signing
endpoint, allowing sandboxed agents to produce signed commits without ever
having direct access to the keys.

```
 +-------------------+          POST /sign/{key-id}         +---------------------+
 |  Sandbox          | ----- payload (HTTP) --------------> |  git-signing-proxy  |
 |  (git commit)     | <---- SSH/GPG signature ------------ |  /etc/signing-keys/ |
 +-------------------+                                      +---------------------+
```

## Features

- **SSH** (SSHSIG) and **GPG** (OpenPGP) signatures, with SSH as the default
- Pure Go signing — no runtime dependency on `ssh-keygen` or `gpg`
- Content-based key detection — point `KEYS_DIR` at `~/.ssh` directly
- Unix socket and TCP listening modes
- Distroless container image, non-root, read-only filesystem
- OpenShift restricted-v2 SCC compatible

## Quick Start

```bash
# Run with default keys from ~/.ssh (Unix socket mode)
make run

# Or run in a container (TCP on localhost:8080)
make run-local

# Test
echo "hello" | curl -sf --data-binary @- http://localhost:8080/sign/id_ed25519
```

## Git Configuration

Configure Git to use the wrapper script as the signing program:

```bash
git config --global gpg.format ssh
git config --global gpg.ssh.program /path/to/scripts/git-signing-proxy.sh
git config --global user.signingKey id_ed25519    # filename in KEYS_DIR
git config --global commit.gpgSign true
```

The wrapper auto-detects the proxy: if `/tmp/claude/git-signing-proxy.sock`
exists it uses the Unix socket, otherwise falls back to `SIGNING_PROXY_URL`
(default `http://git-signing-proxy:8080`). Verification operations are passed
through to the local `ssh-keygen`.

For GPG keys, set `gpg.format openpgp` and `gpg.program` instead of
`gpg.ssh.program`.

## Deploying to OpenShift

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

## API

**`POST /sign/{keyID}`** — Signs the request body and returns the armored
signature. `keyID` matches a filename in the keys directory. Max payload 10 MB.
Returns `400` for invalid input, `404` for unknown key, `500` on failure.

**`GET /healthz`** — Returns `200` if at least one key is loaded, `503`
otherwise.

## Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `KEYS_DIR` | `/etc/signing-keys` | Directory containing private key files |
| `LISTEN_ADDR` | `:8080` | TCP listen address |
| `LISTEN_SOCKET` | *(unset)* | Unix socket path; disables TCP when set |

## Security

- Private keys never leave the proxy — the sandbox only receives signatures
- Key IDs validated against `^[a-zA-Z0-9][a-zA-Z0-9._-]*$`
- Non-root container with read-only filesystem, all capabilities dropped
- Pure Go crypto — no shell, no shell-outs
- Request bodies capped at 10 MB

## License

Apache License 2.0. See [LICENSE](LICENSE) for details.
