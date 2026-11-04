package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestComposePassesRuntimeSwitches is the CI form of the AGENTS orchestration
// rule (47 号 §6.1 提议): a BindEnv'd switch that compose does not pass through
// can never be turned on at the deployment — the backend would simply never see
// it. Both times this shipped as a field bug before any test existed:
//   - P22–P46: the engine's `dns:` (two services had it, the engine did not) —
//     three records ran on a /var/tmp override;
//   - P47: `MACLAW_TENANT_ENGINES` (config bound, compose missing) — the P26a
//     gate had to be verified with a bypass container.
//
// It asserts per-SERVICE blocks (a global substring check passes when the key
// is wired to the wrong container — the exact shape of both failures), and it
// pins only switches whose ABSENCE was already an incident or whose absence
// silently disables a security invariant.

type switchContract struct {
	service string
	key     string
	// want, when set, is the exact expected value (fail-closed constants).
	want string
}

var composeSwitchContracts = []switchContract{
	// 启动断言（assertResponseCacheDisabled / assertNoRemoteGenerationConsent）
	// 把这两个值当安全不变量：compose 不透传 = 后端永远满足不了启动条件。
	{service: "promptfoo-engine", key: "PROMPTFOO_CACHE_ENABLED", want: "0"},
	{service: "promptfoo-engine", key: "REQUEST_TIMEOUT_MS", want: "180000"},
	// 部署参数类：允许 ${VAR:-default} 透传，但必须存在这一行。
	{service: "promptfoo-engine", key: "MACLAW_STRATEGY_CODE_PACKAGES"},
	{service: "backend", key: "MACLAW_TENANT_ENGINES"},
	{service: "backend", key: "PROMPTFOO_ENGINE_BASE_URL"},
}

func composeServiceBlock(t *testing.T, source, service string) string {
	t.Helper()
	// Line state machine (RE2 has no lookahead): a service block starts at the
	// two-space-indented "name:" line and runs to the next same-level key.
	start := "\n  " + service + ":\n"
	idx := strings.Index(source, start)
	if idx < 0 {
		t.Fatalf("compose has no %q service block", service)
	}
	rest := source[idx+len(start):]
	var b strings.Builder
	for _, line := range strings.Split(rest, "\n") {
		if len(line) > 2 && line[0] == ' ' && line[1] == ' ' && line[2] != ' ' && strings.HasSuffix(line, ":") {
			break
		}
		b.WriteString(line)
		b.WriteString("\n")
	}
	if b.Len() == 0 {
		t.Fatalf("compose service %q has an empty block", service)
	}
	return b.String()
}

func TestComposePassesRuntimeSwitches(t *testing.T) {
	path := filepath.Join("..", "..", "docker-compose.yml")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	source := string(raw)
	blocks := map[string]string{}
	for _, c := range composeSwitchContracts {
		if _, ok := blocks[c.service]; !ok {
			blocks[c.service] = composeServiceBlock(t, source, c.service)
		}
		block := blocks[c.service]
		line := ""
		for _, candidate := range strings.Split(block, "\n") {
			trimmed := strings.TrimSpace(candidate)
			if strings.HasPrefix(trimmed, c.key+":") {
				line = trimmed
				break
			}
		}
		if line == "" {
			t.Fatalf("compose service %q does not pass %s — BindEnv reads the CONTAINER env, so without this line the switch can never be turned on at the deployment (47 §6.1 class)", c.service, c.key)
		}
		if c.want != "" {
			value := strings.TrimSpace(strings.SplitN(line, ":", 2)[1])
			if value != `"`+c.want+`"` {
				t.Fatalf("compose %s.%s must stay %q (security invariant asserted at engine boot), got %s", c.service, c.key, c.want, line)
			}
		}
	}
}

// TestComposeEngineCarriesSiteDNS pins the half of the P22 DNS incident that
// was NOT covered by a guard: the engine must resolve the generation-model
// host. The value is site-specific (ai02's in-house resolver); a relocation
// must change all three services together — production-cutover-checklist says
// so, and THIS test is what makes a silent one-sided edit fail CI instead of
// fail in the field with "Generated 0 tests".
func TestComposeEngineCarriesSiteDNS(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "docker-compose.yml"))
	if err != nil {
		t.Fatalf("read compose: %v", err)
	}
	source := string(raw)
	for _, service := range []string{"maclaw-runtime", "backend", "promptfoo-engine"} {
		block := composeServiceBlock(t, source, service)
		if !strings.Contains(block, "dns:") || !strings.Contains(block, "10.95.38.38") {
			t.Fatalf("compose service %q must carry dns: 10.95.38.38 (the daemon's external DNS is unreachable on this network; 43 §8.1)", service)
		}
	}
}

// TestAllBindEnvKeysAppearInCompose is the GENERALIZATION of the point guard
// above: every environment variable the platform binds in pkg/config must be
// reachable in the deployment orchestration — the 46/47 class (engine missing
// dns, backend missing MACLAW_TENANT_ENGINES) is only structurally dead once
// the check is exhaustive rather than curated. An exception must be an
// explicit, justified entry in bindEnvComposeExemptions; empty today on
// purpose.
var bindEnvComposeExemptions = map[string]string{
	// env -> why compose legitimately never mentions it. Adding an entry here
	// is a review-level decision, same weight as deleting a guard assertion.
}

func TestAllBindEnvKeysAppearInCompose(t *testing.T) {
	cfgRaw, err := os.ReadFile(filepath.Join("..", "..", "pkg", "config", "config.go"))
	if err != nil {
		t.Fatalf("read pkg/config/config.go: %v", err)
	}
	composeRaw, err := os.ReadFile(filepath.Join("..", "..", "docker-compose.yml"))
	if err != nil {
		t.Fatalf("read docker-compose.yml: %v", err)
	}
	compose := string(composeRaw)
	bound := map[string]bool{}
	for _, line := range strings.Split(string(cfgRaw), "\n") {
		i := strings.Index(line, "BindEnv(")
		if i < 0 {
			continue
		}
		quotes := []string{}
		run := ""
		in := false
		for _, r := range line[i+len("BindEnv("):] {
			switch {
			case r == '"':
				if in {
					quotes = append(quotes, run)
					run = ""
				}
				in = !in
			case in:
				run += string(r)
			}
		}
		if len(quotes) >= 2 && quotes[1] != "" {
			bound[quotes[1]] = true
		}
	}
	if len(bound) < 20 {
		t.Fatalf("BindEnv inventory implausibly small (%d): parser broken?", len(bound))
	}
	missing := []string{}
	for env := range bound {
		if strings.Contains(compose, env) {
			continue
		}
		if _, ok := bindEnvComposeExemptions[env]; ok {
			continue
		}
		missing = append(missing, env)
	}
	if len(missing) > 0 {
		t.Fatalf("pkg/config binds env vars that docker-compose.yml never passes — they can only be set by hand into containers, and forgetting one is the 46/47 incident class: %v (exempt explicitly in bindEnvComposeExemptions with a justification if a var is genuinely non-deployment)", missing)
	}
}
