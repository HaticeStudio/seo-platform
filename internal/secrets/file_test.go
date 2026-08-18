package secrets

import (
	"context"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/HaticeStudio/seo-platform/core"
)

const testKey = "6368616e676520746869732070617373776f726420746f206120736563726574" // 32 bytes hex

func newFileStore(t *testing.T) (*File, string) {
	t.Helper()
	dir := t.TempDir()
	store, err := NewFile(dir, testKey)
	if err != nil {
		t.Fatal(err)
	}
	return store, dir
}

func TestFileRoundTrip(t *testing.T) {
	store, dir := newFileStore(t)
	ctx := context.Background()
	scope := core.Scope{SiteID: "default", Provider: "fake"}
	ref, err := store.Put(ctx, scope, core.SecretMaterial{Type: "api_key", Bytes: []byte("super-secret")})
	if err != nil {
		t.Fatal(err)
	}

	handle, err := store.Open(ctx, ref, core.PurposeSync)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(handle.Material().Bytes); got != "super-secret" {
		t.Errorf("material = %q", got)
	}
	handle.Close()
	if handle.Material().Bytes != nil {
		t.Error("material readable after Close")
	}

	// Secret must not appear in plaintext on disk.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		raw, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), "super-secret") {
			t.Fatal("plaintext secret found on disk")
		}
		if strings.Contains(string(raw), hex.EncodeToString([]byte("super-secret"))) {
			t.Fatal("hex-encoded secret found on disk")
		}
	}
}

func TestFileRotateAndRevoke(t *testing.T) {
	store, _ := newFileStore(t)
	ctx := context.Background()
	ref, err := store.Put(ctx, core.Scope{SiteID: "default", Provider: "fake"}, core.SecretMaterial{Type: "api_key", Bytes: []byte("v1")})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Rotate(ctx, ref, core.SecretMaterial{Type: "api_key", Bytes: []byte("v2")}); err != nil {
		t.Fatal(err)
	}
	handle, err := store.Open(ctx, ref, core.PurposeSync)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(handle.Material().Bytes); got != "v2" {
		t.Errorf("after rotate material = %q, want v2", got)
	}
	handle.Close()

	if err := store.Revoke(ctx, ref); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Open(ctx, ref, core.PurposeSync); err == nil {
		t.Error("revoked credential still opens")
	}
	if err := store.Revoke(ctx, ref); err != nil {
		t.Errorf("revoke is not idempotent: %v", err)
	}
}

func TestFileRejectsBadKeyAndBadRef(t *testing.T) {
	if _, err := NewFile(t.TempDir(), "short"); err == nil {
		t.Error("bad master key accepted")
	}
	store, _ := newFileStore(t)
	if _, err := store.Open(context.Background(), core.CredentialRef{ID: "../../etc/passwd"}, core.PurposeSync); err == nil {
		t.Error("path-traversal ref accepted")
	}
}

func TestFileWrongMasterKeyFailsToOpen(t *testing.T) {
	dir := t.TempDir()
	store, err := NewFile(dir, testKey)
	if err != nil {
		t.Fatal(err)
	}
	ref, err := store.Put(context.Background(), core.Scope{SiteID: "default", Provider: "fake"}, core.SecretMaterial{Type: "api_key", Bytes: []byte("secret")})
	if err != nil {
		t.Fatal(err)
	}
	other, err := NewFile(dir, strings.Repeat("ab", 32))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := other.Open(context.Background(), ref, core.PurposeSync); err == nil {
		t.Error("wrong master key decrypted the secret")
	}
}
