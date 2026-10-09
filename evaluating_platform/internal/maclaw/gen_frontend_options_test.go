package maclaw

// gen_frontend_options_test.go — dev helper: dumps the frontend
// ENGINE_PLUGIN_OPTIONS / ENGINE_STRATEGY_OPTIONS TS literals from the
// canonical backend catalog (single source of truth).
//   go test ./internal/maclaw/ -run TestDumpFrontendOptions -v
//
// The test always passes; output is only printed with -v.

import (
	"fmt"
	"strings"
	"testing"
)

func TestDumpFrontendOptions(t *testing.T) {
	var b strings.Builder
	b.WriteString("export const ENGINE_PLUGIN_OPTIONS = [\n")
	for _, e := range promptfooPluginCatalog {
		fmt.Fprintf(&b, "  { id: '%s', name: '%s', category: '%s' },\n", e.ID, e.Name, e.Category)
	}
	b.WriteString("] as const\n\n")
	b.WriteString("export const ENGINE_STRATEGY_OPTIONS = [\n")
	for _, e := range promptfooStrategyCatalog {
		fmt.Fprintf(&b, "  { id: '%s', name: '%s' },\n", e.ID, e.Name)
	}
	b.WriteString("] as const\n")
	t.Log("\n" + b.String())
}
