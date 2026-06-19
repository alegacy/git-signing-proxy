# git-signing-proxy

A lightweight HTTP service that signs Git commits on behalf of isolated sandbox
environments. It holds private SSH or GPG keys and exposes a single signing
endpoint, allowing sandboxed agents to produce signed commits without ever
having direct access to the keys.

## Why

AI agents running inside NVIDIA OpenShell sandboxes (or similar locked-down
environments) face strict egress and filesystem policies. They cannot hold
private signing keys or run `ssh-keygen` / `gpg` directly. This proxy runs
alongside the sandbox — either in a local container or as an OpenShift
service — and handles the cryptographic signing over a simple HTTP POST.

```
 +-------------------+          POST /sign/{key-id}         +---------------------+
 |  OpenShell        | ----- payload (HTTP) --------------> |  git-signing-proxy  |
 |  sandbox          |                                      |                     |
 |  (git commit)     | <---- SSH/GPG signature ------------ |  /etc/signing-keys/ |
 +-------------------+                                      +---------------------+
```

## Features

- Supports both **SSH** (SSHSIG / PROTOCOL.sshsig) and **GPG** (OpenPGP armored
  detached) signatures, with SSH as the default
- Pure Go signing — no runtime dependency on `ssh-keygen` or `gpg`
- Loads multiple keys from a directory; the `{key-id}` in the URL maps to the
  filename
- Distroless container image, non-root, read-only filesystem
- OpenShift restricted-v2 SCC compatible
- Graceful shutdown on SIGTERM
- Structured logging via `slog`

## Quick Start

### Prerequisites

- Go 1.25+ (auto-downloaded via toolchain directive)
- `podman` or `docker`
- An SSH or GPG private key (unencrypted)

### Generate a test key

```bash
mkdir -p ~/signing-keys
ssh-keygen -t ed25519 -f ~/signing-keys/my-key -N "" -C "agent@example.com"
```

### Run locally

```bash
# Run the binary directly
make run KEYS_DIR=~/signing-keys

# Or run in a container
make run-local KEYS_DIR=~/signing-keys
```

### Test the endpoint

```bash
echo "hello world" | curl -sf --data-binary @- http://localhost:8080/sign/my-key
```

You should see an armored SSH signature:

```
-----BEGIN SSH SIGNATURE-----
U1NIU0lHAAAAAQAAADMAAAALc3NoLWVkMjU1MTkAAAAg...
-----END SSH SIGNATURE-----
```

## Git Configuration

### Inside the sandbox

Copy `scripts/git-signing-proxy.sh` into the sandbox and configure Git to use
it as the signing program:

```bash
export SIGNING_PROXY_URL="http://git-signing-proxy:8080"  # adjust to match your setup

git config --global gpg.format ssh
git config --global gpg.ssh.program /path/to/git-signing-proxy.sh
git config --global user.signingKey my-key           # matches the filename in /etc/signing-keys/
git config --global commit.gpgSign true
git config --global tag.gpgSign true
```

For GPG keys instead of SSH:

```bash
git config --global gpg.format openpgp
git config --global gpg.program /path/to/git-signing-proxy.sh
git config --global user.signingKey my-gpg-key
git config --global commit.gpgSign true
```

### Signature verification

The wrapper script passes verification operations (`-Y verify`,
`-Y find-principals`, `-Y check-novalidate`) through to the local `ssh-keygen`,
so `git log --show-signature` works if the sandbox has `ssh-keygen` and an
allowed signers file:

```bash
git config --global gpg.ssh.allowedSignersFile ~/.ssh/allowed_signers
```

The allowed signers file maps identities to public keys:

```
agent@example.com ssh-ed25519 AAAAC3Nz...
```

## Deploying to OpenShift

### 1. Build and push the image

```bash
make docker-build docker-push IMG=quay.io/youruser/git-signing-proxy:latest
```

### 2. Create the signing key secret

```bash
make create-secret \
  SECRET_KEY_FILE=~/signing-keys/my-key \
  SECRET_KEY_ID=my-key
```

To add multiple keys to the same secret:

```bash
oc create secret generic git-signing-keys \
  --from-file=my-ssh-key=~/.ssh/id_ed25519 \
  --from-file=my-gpg-key=~/keys/gpg-private.asc \
  --dry-run=client -o yaml | oc apply -f -
```

### 3. Deploy

```bash
make deploy IMG=quay.io/youruser/git-signing-proxy:latest
```

### 4. Verify

```bash
oc get pods -l app.kubernetes.io/name=git-signing-proxy
oc logs deployment/git-signing-proxy
```

### Undeploy

```bash
make undeploy
```

## API

### `POST /sign/{keyID}`

Signs the request body and returns the armored signature.

| Field | Value |
|-------|-------|
| **Method** | `POST` |
| **Path** | `/sign/{keyID}` — `keyID` matches a filename in the keys directory |
| **Body** | Raw bytes to sign (max 10 MB) |
| **Response** | `text/plain` — armored SSH or GPG signature |
| **Errors** | `400` invalid/empty input, `404` unknown key, `500` signing failure |

Example:

```bash
curl -sf --data-binary @file-to-sign http://localhost:8080/sign/my-key
```

### `GET /healthz`

Returns `200 ok` if the server has at least one signing key loaded, or
`503` if no keys are available.

## Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `KEYS_DIR` | `/etc/signing-keys` | Directory containing private key files |
| `LISTEN_ADDR` | `:8080` | Address and port to listen on |

## Makefile Targets

```
make help
```

| Target | Description |
|--------|-------------|
| `build` | Build the binary |
| `test` | Run unit tests with race detection |
| `fmt` | Format source code |
| `vet` | Run `go vet` |
| `lint` | Run `golangci-lint` and `go vet` |
| `tidy` | Tidy and verify Go modules |
| `clean` | Remove build artifacts |
| `docker-build` | Build container image |
| `docker-push` | Push container image |
| `run` | Run the binary locally |
| `run-local` | Run in a local container with mounted keys |
| `stop-local` | Stop the local container |
| `deploy` | Deploy to OpenShift |
| `undeploy` | Remove from OpenShift |
| `create-secret` | Create the signing key Kubernetes secret |

## Key Format Support

The service auto-detects key type by inspecting the file content:

| Format | Detected by | Signing output |
|--------|-------------|----------------|
| OpenSSH (ed25519, RSA, ECDSA) | `PRIVATE KEY` header | SSHSIG armored block |
| PEM (RSA, EC, DSA) | `PRIVATE KEY` header | SSHSIG armored block |
| GPG armored | `PGP PRIVATE KEY` header | PGP armored detached signature |
| GPG binary | Binary keyring probe | PGP armored detached signature |

Keys must be **unencrypted** (no passphrase). Public key files (`.pub`) are
automatically skipped.

## Security

- **Key isolation**: Private keys never leave the proxy. The sandbox only
  receives signatures.
- **Path traversal protection**: Key IDs are validated against
  `^[a-zA-Z0-9][a-zA-Z0-9._-]*$` and resolved within the keys directory.
- **Non-root container**: Runs as a non-root user with a read-only root
  filesystem, all capabilities dropped, and seccomp RuntimeDefault profile.
- **No shell-outs**: All signing is done in-process using Go crypto libraries.
  The container image has no shell.
- **Payload limits**: Request bodies are capped at 10 MB.

## License

Apache License 2.0. See [LICENSE](LICENSE) for details.
