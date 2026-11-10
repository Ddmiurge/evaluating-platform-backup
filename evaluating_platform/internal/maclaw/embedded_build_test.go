package maclaw

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// E-4 staging: the image only ever contains what the CURRENT guard accepts,
// and the index is the harness's single source of "what's baked".
func TestStageEmbeddedPackagesBuildsTreeAndIndex(t *testing.T) {
	dir := t.TempDir()
	index, err := StageEmbeddedPackages([]EmbeddedBuildInput{
		{PackageID: "probe-demo", PayloadB64: validZip(t)},
	}, dir, "batch-42")
	if err != nil {
		t.Fatalf("stage: %v", err)
	}
	if len(index.Packages) != 1 || index.Packages[0].ID != "probe-demo" || index.Packages[0].Fanout != 1 {
		t.Fatalf("index = %+v", index)
	}
	if index.BatchID != "batch-42" || index.BuiltAt.IsZero() {
		t.Fatalf("batch stamp = %+v", index)
	}
	// The tree carries the package's own files…
	if _, err := os.Stat(filepath.Join(dir, "probe-demo", "tests", "run.mjs")); err != nil {
		t.Fatalf("staged test entry missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "probe-demo", "src", "plugin.ts")); err != nil {
		t.Fatalf("staged entry missing: %v", err)
	}
	// …and index.json is what the engine loader will read.
	raw, err := os.ReadFile(filepath.Join(dir, "index.json"))
	if err != nil {
		t.Fatalf("index.json: %v", err)
	}
	var parsed EmbeddedBuildIndex
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("index.json parse: %v", err)
	}
	if len(parsed.Packages) != 1 || parsed.Packages[0].Name == "" {
		t.Fatalf("index.json = %+v", parsed)
	}
}

func TestStageEmbeddedPackagesRefusesWhatTheCurrentGuardRefuses(t *testing.T) {
	dir := t.TempDir()

	t.Run("id mismatch between state row and manifest", func(t *testing.T) {
		_, err := StageEmbeddedPackages([]EmbeddedBuildInput{
			{PackageID: "someone-elses-id", PayloadB64: validZip(t)},
		}, dir, "b")
		if err == nil {
			t.Fatal("expected refusal")
		}
	})

	t.Run("package that fails today's capability-token rule", func(t *testing.T) {
		payload := buildZip(t, []zipEntry{
			{"manifest.yaml", validManifest},
			{"src/plugin.ts", "const k = process.env.SECRET;"},
			{"tests/run.mjs", "process.exit(0)"},
		})
		_, err := StageEmbeddedPackages([]EmbeddedBuildInput{{PackageID: "probe-demo", PayloadB64: payload}}, dir, "b")
		if err == nil {
			t.Fatal("a payload refused by today's rules must not reach the image tree")
		}
	})

	t.Run("duplicate ids in one batch", func(t *testing.T) {
		_, err := StageEmbeddedPackages([]EmbeddedBuildInput{
			{PackageID: "probe-demo", PayloadB64: validZip(t)},
			{PackageID: "probe-demo", PayloadB64: validZip(t)},
		}, dir, "b")
		if err == nil {
			t.Fatal("expected refusal")
		}
	})
}
