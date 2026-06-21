package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha512"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/ProtonMail/go-crypto/openpgp"
	"golang.org/x/crypto/ssh"
)

const maxPayloadSize = 10 << 20 // 10 MB

var validKeyID = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]*$`)

type keyKind int

const (
	keyKindSSH keyKind = iota
	keyKindGPG
)

func (k keyKind) String() string {
	switch k {
	case keyKindSSH:
		return "ssh"
	case keyKindGPG:
		return "gpg"
	default:
		return "unknown"
	}
}

type signingKey struct {
	id        string
	kind      keyKind
	sshSigner ssh.Signer
	gpgEntity *openpgp.Entity
}

type server struct {
	keys    map[string]*signingKey
	keysDir string
}

func newServer(keysDir string) (*server, error) {
	s := &server{
		keys:    make(map[string]*signingKey),
		keysDir: keysDir,
	}
	if err := s.loadKeys(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *server) loadKeys() error {
	entries, err := os.ReadDir(s.keysDir)
	if err != nil {
		return fmt.Errorf("reading keys directory %s: %w", s.keysDir, err)
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if strings.HasPrefix(name, ".") || strings.HasSuffix(name, ".pub") {
			continue
		}
		path := filepath.Join(s.keysDir, name)

		data, err := os.ReadFile(path)
		if err != nil {
			slog.Debug("skipping unreadable file", "file", name, "error", err)
			continue
		}

		if !strings.Contains(string(data), "PRIVATE KEY") {
			continue
		}

		key, err := parseKey(name, data)
		if err != nil {
			slog.Warn("failed to parse private key", "file", name, "error", err)
			continue
		}

		s.keys[name] = key
		slog.Info("loaded signing key", "id", name, "type", key.kind)
	}

	if len(s.keys) == 0 {
		return fmt.Errorf("no valid signing keys found in %s", s.keysDir)
	}

	return nil
}

func parseKey(id string, data []byte) (*signingKey, error) {
	content := string(data)

	if strings.Contains(content, "PGP PRIVATE KEY") {
		return parseGPGKey(id, data)
	}

	if strings.Contains(content, "PRIVATE KEY") {
		return parseSSHKey(id, data)
	}

	// Try binary GPG
	if key, err := parseGPGKey(id, data); err == nil {
		return key, nil
	}

	// Fallback to SSH
	return parseSSHKey(id, data)
}

func parseSSHKey(id string, data []byte) (*signingKey, error) {
	signer, err := ssh.ParsePrivateKey(data)
	if err != nil {
		return nil, fmt.Errorf("parsing SSH key: %w", err)
	}
	return &signingKey{id: id, kind: keyKindSSH, sshSigner: signer}, nil
}

func parseGPGKey(id string, data []byte) (*signingKey, error) {
	entities, err := openpgp.ReadArmoredKeyRing(bytes.NewReader(data))
	if err != nil {
		entities, err = openpgp.ReadKeyRing(bytes.NewReader(data))
		if err != nil {
			return nil, fmt.Errorf("parsing GPG key: %w", err)
		}
	}
	if len(entities) == 0 {
		return nil, fmt.Errorf("no GPG entities found")
	}
	entity := entities[0]
	if entity.PrivateKey == nil {
		return nil, fmt.Errorf("no private key in GPG entity")
	}
	if entity.PrivateKey.Encrypted {
		return nil, fmt.Errorf("GPG key is passphrase-protected; provide an unencrypted key")
	}
	return &signingKey{id: id, kind: keyKindGPG, gpgEntity: entity}, nil
}

// --- SSH Signature (SSHSIG / PROTOCOL.sshsig) ---

func writeSSHString(buf *bytes.Buffer, data []byte) {
	_ = binary.Write(buf, binary.BigEndian, uint32(len(data)))
	buf.Write(data)
}

func sshSignPayload(signer ssh.Signer, message []byte) ([]byte, error) {
	const namespace = "git"
	const hashAlgo = "sha512"

	h := sha512.Sum512(message)

	var signedData bytes.Buffer
	signedData.Write([]byte("SSHSIG"))
	writeSSHString(&signedData, []byte(namespace))
	writeSSHString(&signedData, nil) // reserved
	writeSSHString(&signedData, []byte(hashAlgo))
	writeSSHString(&signedData, h[:])

	sig, err := sshSignWithBestAlgo(signer, signedData.Bytes())
	if err != nil {
		return nil, fmt.Errorf("signing: %w", err)
	}

	sigBlob := ssh.Marshal(sig)

	var blob bytes.Buffer
	blob.Write([]byte("SSHSIG"))
	_ = binary.Write(&blob, binary.BigEndian, uint32(1)) // version
	writeSSHString(&blob, signer.PublicKey().Marshal())
	writeSSHString(&blob, []byte(namespace))
	writeSSHString(&blob, nil) // reserved
	writeSSHString(&blob, []byte(hashAlgo))
	writeSSHString(&blob, sigBlob)

	return armorSSHSignature(blob.Bytes()), nil
}

func sshSignWithBestAlgo(signer ssh.Signer, data []byte) (*ssh.Signature, error) {
	if as, ok := signer.(ssh.AlgorithmSigner); ok && signer.PublicKey().Type() == ssh.KeyAlgoRSA {
		return as.SignWithAlgorithm(rand.Reader, data, ssh.KeyAlgoRSASHA512)
	}
	return signer.Sign(rand.Reader, data)
}

func armorSSHSignature(data []byte) []byte {
	encoded := base64.StdEncoding.EncodeToString(data)

	var buf bytes.Buffer
	buf.WriteString("-----BEGIN SSH SIGNATURE-----\n")
	for i := 0; i < len(encoded); i += 76 {
		end := i + 76
		if end > len(encoded) {
			end = len(encoded)
		}
		buf.WriteString(encoded[i:end])
		buf.WriteByte('\n')
	}
	buf.WriteString("-----END SSH SIGNATURE-----\n")
	return buf.Bytes()
}

// --- GPG Signature ---

func gpgSignPayload(entity *openpgp.Entity, message []byte) ([]byte, error) {
	var sig bytes.Buffer
	if err := openpgp.ArmoredDetachSign(&sig, entity, bytes.NewReader(message), nil); err != nil {
		return nil, fmt.Errorf("gpg signing: %w", err)
	}
	return sig.Bytes(), nil
}

// --- HTTP Handlers ---

func (s *server) handleSign(w http.ResponseWriter, r *http.Request) {
	keyID := r.PathValue("keyID")

	if !validKeyID.MatchString(keyID) {
		slog.Warn("rejected invalid key ID", "key_id", keyID, "remote", r.RemoteAddr)
		http.Error(w, "invalid key ID", http.StatusBadRequest)
		return
	}

	key, ok := s.keys[keyID]
	if !ok {
		slog.Warn("key not found", "key_id", keyID, "remote", r.RemoteAddr)
		http.Error(w, "key not found", http.StatusNotFound)
		return
	}

	payload, err := io.ReadAll(io.LimitReader(r.Body, maxPayloadSize+1))
	if err != nil {
		slog.Error("reading request body", "error", err)
		http.Error(w, "failed to read request body", http.StatusBadRequest)
		return
	}
	if len(payload) == 0 {
		http.Error(w, "empty payload", http.StatusBadRequest)
		return
	}
	if len(payload) > maxPayloadSize {
		http.Error(w, "payload too large", http.StatusRequestEntityTooLarge)
		return
	}

	var sig []byte
	switch key.kind {
	case keyKindSSH:
		sig, err = sshSignPayload(key.sshSigner, payload)
	case keyKindGPG:
		sig, err = gpgSignPayload(key.gpgEntity, payload)
	}
	if err != nil {
		slog.Error("signing failed", "key_id", keyID, "type", key.kind, "error", err)
		http.Error(w, "signing failed", http.StatusInternalServerError)
		return
	}

	slog.Info("signed payload", "key_id", keyID, "type", key.kind, "size", len(payload))
	w.Header().Set("Content-Type", "text/plain")
	_, _ = w.Write(sig)
}

func (s *server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	if len(s.keys) == 0 {
		http.Error(w, "no signing keys loaded", http.StatusServiceUnavailable)
		return
	}
	_, _ = io.WriteString(w, "ok\n")
}

func main() {
	keysDir := os.Getenv("KEYS_DIR")
	if keysDir == "" {
		keysDir = "/etc/signing-keys"
	}

	listenAddr := os.Getenv("LISTEN_ADDR")
	if listenAddr == "" {
		listenAddr = ":8080"
	}

	listenSocket := os.Getenv("LISTEN_SOCKET")

	absKeysDir, err := filepath.Abs(keysDir)
	if err != nil {
		slog.Error("resolving keys directory", "error", err)
		os.Exit(1)
	}

	srv, err := newServer(absKeysDir)
	if err != nil {
		slog.Error("initializing server", "error", err)
		os.Exit(1)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /sign/{keyID}", srv.handleSign)
	mux.HandleFunc("GET /healthz", srv.handleHealthz)

	httpServer := &http.Server{
		Handler:      mux,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	shutdown := make(chan os.Signal, 1)
	signal.Notify(shutdown, os.Interrupt, syscall.SIGTERM)

	go func() {
		var err error
		if listenSocket != "" {
			os.Remove(listenSocket)
			var ln net.Listener
			ln, err = net.Listen("unix", listenSocket)
			if err != nil {
				slog.Error("creating unix socket", "path", listenSocket, "error", err)
				os.Exit(1)
			}
			if err := os.Chmod(listenSocket, 0600); err != nil {
				slog.Error("setting socket permissions", "path", listenSocket, "error", err)
				os.Exit(1)
			}
			slog.Info("starting git-signing-proxy",
				"socket", listenSocket,
				"keys_dir", absKeysDir,
				"loaded_keys", len(srv.keys),
			)
			err = httpServer.Serve(ln)
		} else {
			httpServer.Addr = listenAddr
			slog.Info("starting git-signing-proxy",
				"addr", listenAddr,
				"keys_dir", absKeysDir,
				"loaded_keys", len(srv.keys),
			)
			err = httpServer.ListenAndServe()
		}
		if err != nil && err != http.ErrServerClosed {
			slog.Error("server stopped", "error", err)
			os.Exit(1)
		}
	}()

	sig := <-shutdown
	slog.Info("shutting down", "signal", sig)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := httpServer.Shutdown(ctx); err != nil {
		slog.Error("graceful shutdown failed", "error", err)
		os.Exit(1)
	}

	if listenSocket != "" {
		os.Remove(listenSocket)
	}
}
