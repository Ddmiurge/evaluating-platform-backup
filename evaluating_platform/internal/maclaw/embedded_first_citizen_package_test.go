package maclaw

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// E-6 首公民包（records 计划：语义缓存包走完全程）。
//
// The first-citizen package lives at the workspace root
// (embedded-packages/semantic-cache-collision) — expert-package source, NOT
// staging (the staging tree is decrypted runtime state and stays gitignored).
// This test zips it exactly as an upload would and pushes it through the SAME
// store exit an expert upload hits: zip safety → capability tokens → manifest
// contract → catalog rules. If the package drifts (a banned token sneaks into
// a comment, the base version bumps, the id starts colliding), the upload on
// the machine would refuse it — better red here than a failed E-6 step.
func TestSemanticCacheCollisionPackagePassesStoreExit(t *testing.T) {
	dir := filepath.Join("..", "..", "..", "embedded-packages", "semantic-cache-collision")
	if _, err := os.Stat(dir); err != nil {
		t.Skipf("first-citizen package not present at %s: %v", dir, err)
	}

	var entries []zipEntry
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		entries = append(entries, zipEntry{name: filepath.ToSlash(rel), content: string(content)})
		return nil
	})
	if err != nil {
		t.Fatalf("walk package dir: %v", err)
	}
	if len(entries) < 4 {
		t.Fatalf("package looks incomplete: %d files", len(entries))
	}

	raw, err := DecodeEmbeddedPayload(buildZip(t, entries))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	manifest, stats, err := ValidateEmbeddedPluginPackage(raw)
	if err != nil {
		t.Fatalf("store exit refused the first-citizen package: %v", err)
	}
	if err := ValidateEmbeddedManifestForCatalog(manifest, EmbeddedPluginIDReserved); err != nil {
		t.Fatalf("catalog rules refused the first-citizen package: %v", err)
	}

	if manifest.ID != "semantic-cache-collision" {
		t.Errorf("id = %q", manifest.ID)
	}
	if manifest.Category != "privacy" {
		t.Errorf("category = %q, want privacy (cross-user leak)", manifest.Category)
	}
	if manifest.Tests != EmbeddedPluginTestEntry {
		t.Errorf("tests = %q, want %q", manifest.Tests, EmbeddedPluginTestEntry)
	}
	if stats.Files < 4 {
		t.Errorf("files = %d, want >= 4 (manifest/README/entry/tests)", stats.Files)
	}
	if stats.SourceBytes <= 0 {
		t.Error("source_bytes must be positive")
	}

	// The three files the runtime contract depends on must be present by name.
	names := map[string]bool{}
	for _, e := range entries {
		names[e.name] = true
	}
	for _, want := range []string{"manifest.yaml", "src/plugin.ts", "tests/run.mjs"} {
		if !names[want] {
			t.Errorf("zip missing %s", want)
		}
	}
}

// The package's own test entry is what the upload gate executes in a bounded
// child process (`node tests/run.mjs`). Running the same command here proves
// the gate would pass on the machine — offline, zero-dep, exit code contract.
func TestSemanticCacheCollisionPackageOwnTestsPass(t *testing.T) {
	dir := filepath.Join("..", "..", "..", "embedded-packages", "semantic-cache-collision")
	if _, err := os.Stat(dir); err != nil {
		t.Skipf("first-citizen package not present at %s: %v", dir, err)
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not available on this machine")
	}
	cmd := exec.Command(node, "tests/run.mjs")
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("package tests failed: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "all checks passed") {
		t.Fatalf("unexpected test output: %s", out)
	}
}
