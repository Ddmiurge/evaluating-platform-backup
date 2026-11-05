package handler

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// The admin account list emits engine_instance/model_override keys and the
// FE account page reads them; the register route string lives in three files.
// Any one-sided rename would silently strip the panel (badges just stop
// appearing — no error anywhere), so pin the contract at source level, the way
// every other FE/BE shared literal is pinned in this repo.
func TestFrontendAdminEnginePanelMatchesBackendKeys(t *testing.T) {
	_, thisFile, _, _ := runtime.Caller(0)
	read := func(parts ...string) string {
		raw, err := os.ReadFile(filepath.Join(append([]string{filepath.Dir(thisFile)}, parts...)...))
		if err != nil {
			t.Fatalf("read %v: %v", parts, err)
		}
		return string(raw)
	}
	feService := read("..", "..", "..", "frontend", "src", "services", "admin.ts")
	fePage := read("..", "..", "..", "frontend", "src", "pages", "admin", "AccountTenantManagement.tsx")
	for _, literal := range []string{"engine_instance", "model_override", "/engine-instance"} {
		if !strings.Contains(feService, literal) {
			t.Fatalf("admin.ts must keep reading backend key %q", literal)
		}
	}
	for _, literal := range []string{"engine_instance", "model_override", "registerAccountEngine"} {
		if !strings.Contains(fePage, literal) {
			t.Fatalf("account page must keep using %q", literal)
		}
	}
	feForm := read("..", "..", "..", "frontend", "src", "pages", "admin", "engineInstanceForm.ts")
	if !strings.Contains(feForm, "token_mismatch") {
		t.Fatal("engineInstanceForm must keep the token_mismatch literal (P26c.2 probe state)")
	}
	probeSrc, err := os.ReadFile(filepath.Join(filepath.Dir(thisFile), "..", "..", "maclaw", "engine_instance_admin.go"))
	if err != nil {
		t.Fatalf("read engine_instance_admin.go: %v", err)
	}
	if !strings.Contains(string(probeSrc), "token_mismatch") {
		t.Fatal("backend probe must keep the token_mismatch literal (51 §8.1 silent-401 state)")
	}
	handlerSrc := read("admin_maclaw.go")
	for _, literal := range []string{`"engine_instance"`, `"model_override"`} {
		if !strings.Contains(handlerSrc, literal) {
			t.Fatalf("ListAccounts must keep emitting %s", literal)
		}
	}
}
