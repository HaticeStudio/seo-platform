package main

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGeneratedBootstrapKeyMatchesStoredHash(t *testing.T) {
	dir := t.TempDir()
	hashFile := filepath.Join(dir, "auth", "key.sha256")
	tokenFile := filepath.Join(dir, "bootstrap", "key")
	spec, err := loadOrCreateBootstrapAPIKey(hashFile, tokenFile)
	if err != nil {
		t.Fatal(err)
	}
	token, err := os.ReadFile(tokenFile)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(strings.TrimSpace(string(token))))
	wantPrefix := hex.EncodeToString(sum[:]) + "="
	if !strings.HasPrefix(spec, wantPrefix) {
		t.Fatalf("stored hash does not authenticate generated token")
	}
	for _, filename := range []string{hashFile, tokenFile} {
		info, err := os.Stat(filename)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm()&0o077 != 0 {
			t.Errorf("%s permissions are %o, want owner-only", filename, info.Mode().Perm())
		}
	}
	// Loading an existing installation must not create a fresh plaintext token.
	if err := os.Remove(tokenFile); err != nil {
		t.Fatal(err)
	}
	second, err := loadOrCreateBootstrapAPIKey(hashFile, tokenFile)
	if err != nil || second != spec {
		t.Fatalf("reload = %q, %v; want %q", second, err, spec)
	}
	if _, err := os.Stat(tokenFile); !os.IsNotExist(err) {
		t.Fatalf("plaintext bootstrap token was recreated: %v", err)
	}
}

func TestLoadOrCreateHexSecretRejectsMalformedExistingKey(t *testing.T) {
	filename := filepath.Join(t.TempDir(), "master-key")
	if err := os.WriteFile(filename, []byte("not-a-key"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadOrCreateHexSecret(filename, 32); err == nil {
		t.Fatal("malformed existing key was silently replaced")
	}
}
