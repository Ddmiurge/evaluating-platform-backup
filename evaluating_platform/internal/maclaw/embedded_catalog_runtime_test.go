package maclaw

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// P28 卷挂载 · runtime wizard-catalog source (EMBEDDED_CATALOG_FILE).
//
// The contract under test: a set path WINS over the compiled-in embed and
// fails loud when unusable; an empty path is byte-for-byte pre-volume
// behavior. 反证: deleting the env branch in embeddedCatalogFromEnv collapses
// every case onto the fallback — TestRuntimeFileWins goes red with 0 plugins
// (the wizard would serve the embed while the engine serves the volume).

func TestEmbeddedCatalogRuntimeFileWinsOverEmbed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "embedded_plugins.json")
	doc := `{"promptfoo_version":"0.123.0","plugins":[{"id":"p28-vol","name":"卷探针","category":"injection","severity":"high","fanout":1,"entry":"src/plugin.ts"}]}`
	if err := os.WriteFile(path, []byte(doc), 0o644); err != nil {
		t.Fatalf("write runtime catalog: %v", err)
	}

	got := embeddedCatalogFromEnv(path, []byte(`{"promptfoo_version":"0.123.0","plugins":[]}`))
	if len(got.Plugins) != 1 || got.Plugins[0].ID != "p28-vol" {
		t.Fatalf("runtime file must win over the embed: got %+v", got.Plugins)
	}
	if got.PromptfooVersion != "0.123.0" {
		t.Fatalf("runtime file's promptfoo_version must be kept (base-mismatch gate reads it): got %q", got.PromptfooVersion)
	}
}

func TestEmbeddedCatalogUnsetFallsBackToEmbed(t *testing.T) {
	got := embeddedCatalogFromEnv("", []byte(`{"promptfoo_version":"0.999.0","plugins":[{"id":"from-embed","name":"n","category":"c","severity":"low","fanout":1}]}`))
	if len(got.Plugins) != 1 || got.Plugins[0].ID != "from-embed" || got.PromptfooVersion != "0.999.0" {
		t.Fatalf("empty path must parse the fallback byte-for-byte: got %+v", got)
	}
	// Whitespace-only counts as unset (TrimSpace), not as a broken path.
	got = embeddedCatalogFromEnv("   ", []byte(`{"promptfoo_version":"x","plugins":[]}`))
	if got.PromptfooVersion != "x" {
		t.Fatalf("whitespace path must count as unset, got %q", got.PromptfooVersion)
	}
}

// Fail-closed: a set-but-unusable path must refuse to boot (panic), never
// degrade to the embed — the embed would silently show a catalog the volume no
// longer matches (two views of one fact), and a docker dir-instead-of-file
// mount materializes as EISDIR, which lands here too.
func TestEmbeddedCatalogRefusesUnusableRuntimeFile(t *testing.T) {
	dir := t.TempDir()

	cases := []struct {
		name string
		path string
		body string // "" = create nothing (missing file)
		want string
	}{
		{name: "missing", path: filepath.Join(dir, "never-written.json"), body: "", want: "is set but unreadable"},
		{name: "malformed", path: filepath.Join(dir, "bad.json"), body: "{not json", want: "is malformed"},
		{name: "wrong shape at mount point", path: dir, body: "", want: "is set but unreadable"}, // EISDIR: bind source came up as a directory
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.body != "" {
				if err := os.WriteFile(tc.path, []byte(tc.body), 0o644); err != nil {
					t.Fatalf("write: %v", err)
				}
			}
			defer func() {
				r := recover()
				if r == nil {
					t.Fatalf("must panic (refuse to boot), got none")
				}
				msg, _ := r.(string)
				if !strings.Contains(msg, tc.want) {
					t.Fatalf("panic message must contain %q, got %q", tc.want, msg)
				}
				if !strings.Contains(msg, EmbeddedCatalogFileEnv) && tc.want != "is malformed" {
					// malformed case names the source already; the unreadable case
					// must name the env var so an operator knows what to fix.
					t.Fatalf("unreadable panic must name %s, got %q", EmbeddedCatalogFileEnv, msg)
				}
			}()
			_ = embeddedCatalogFromEnv(tc.path, []byte(`{"promptfoo_version":"0.123.0","plugins":[]}`))
		})
	}
}
