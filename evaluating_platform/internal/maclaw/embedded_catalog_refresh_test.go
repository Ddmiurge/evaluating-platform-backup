package maclaw

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

// E-4d-3 · hot refresh of the wizard catalog (design §14.5-4).
//
// The SIGHUP handler calls RefreshEmbeddedCatalog. Two halves of the receipt
// contract: a GOOD file swaps version + content + hash together, and a BAD
// file (malformed or missing) must leave the PREVIOUS snapshot serving — a
// broken batch marker must never blank the wizard catalog.
func TestRefreshEmbeddedCatalogSwapsAndFailsClosed(t *testing.T) {
	origCatalog := embeddedPluginCatalog
	origVersion := embeddedPluginCatalogVersion
	origHash := embeddedPluginCatalogHash
	t.Cleanup(func() {
		embeddedPluginCatalog = origCatalog
		embeddedPluginCatalogVersion = origVersion
		embeddedPluginCatalogHash = origHash
	})

	dir := t.TempDir()
	path := filepath.Join(dir, "catalog.json")
	t.Setenv(EmbeddedCatalogFileEnv, path)

	good := `{"promptfoo_version":"0.123.0","plugins":[{"id":"hot-x","name":"热插件","category":"injection","severity":"high","fanout":1}]}`
	if err := os.WriteFile(path, []byte(good), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := RefreshEmbeddedCatalog(); err != nil {
		t.Fatalf("refresh with a good file: %v", err)
	}
	if got := EmbeddedPluginCatalog(); len(got) != 1 || got[0].ID != "hot-x" {
		t.Fatalf("catalog = %+v, want the hot-x entry", got)
	}
	version, hash := EmbeddedCatalogStatus()
	wantHash := sha256.Sum256([]byte(good))
	if hash != hex.EncodeToString(wantHash[:]) {
		t.Fatalf("hash = %q, want sha256 of the loaded document", hash)
	}
	if version != "0.123.0" {
		t.Fatalf("version = %q, want 0.123.0", version)
	}

	// Malformed: error out, snapshot untouched (plugins AND hash AND version).
	if err := os.WriteFile(path, []byte("{broken json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := RefreshEmbeddedCatalog(); err == nil {
		t.Fatal("a malformed catalog must fail the refresh")
	}
	if got := EmbeddedPluginCatalog(); len(got) != 1 || got[0].ID != "hot-x" {
		t.Fatalf("failed refresh mutated the snapshot: %+v", got)
	}
	if _, hashAfter := EmbeddedCatalogStatus(); hashAfter != hash {
		t.Fatalf("failed refresh changed the hash: %q → %q", hash, hashAfter)
	}

	// Missing file: same fail-closed rule (a deleted file is not a valid
	// "empty catalog" — the boot path would have refused to start).
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := RefreshEmbeddedCatalog(); err == nil {
		t.Fatal("a missing catalog file must fail the refresh")
	}
	if got := EmbeddedPluginCatalog(); len(got) != 1 || got[0].ID != "hot-x" {
		t.Fatalf("failed refresh mutated the snapshot: %+v", got)
	}
}

// The boot-time hash must describe the bytes the boot load actually parsed —
// otherwise /health would claim a receipt that never matched any file.
func TestEmbeddedCatalogStatusCoversBootSnapshot(t *testing.T) {
	version, hash := EmbeddedCatalogStatus()
	if version != embeddedPluginFileData.PromptfooVersion {
		t.Fatalf("status version = %q, boot loaded %q", version, embeddedPluginFileData.PromptfooVersion)
	}
	if len(hash) != 64 {
		t.Fatalf("hash = %q, want 64 hex chars", hash)
	}
}
