package maclaw

// gateway_contract_test.go — U2 / T3.1: interface ↔ route contract test.
//
// Every method on the gateway interfaces below is the boundary between the
// platform and the MaClaw upstream API. U3 will physically split client.go and
// narrow these interfaces; that refactor must not silently drop or invent a
// method. This test is the safety net: it enumerates each interface's method
// set with reflect and asserts it is exactly the set registered in the
// route/exemption tables in this file (assertion is bidirectional).
//
// The registered value documents the concrete upstream HTTP endpoint the
// Client implementation calls for that method (the "route"), or an explicit
// "exempt: <reason>" for pure-local methods that never touch the wire
// (internal helpers / configuration-only checks). The route strings are
// documentation, not assertions — what is asserted is that EVERY interface
// method has an entry and EVERY entry maps to a real method, so a new method
// cannot land without a deliberate "here is its route / here is why it has
// none" decision.

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// evaluationGatewayRoutes registers every EvaluationGateway method against its
// upstream route (see client.go for the concrete doJSON calls).
var evaluationGatewayRoutes = map[string]string{
	"Enabled":                   "exempt: pure local configuration check (no I/O)",
	"SearchEvaluationResources": "GET /api/v1/evaluation/resources",
	"SaveEvaluationResource":    "POST /api/v1/evaluation/resources",
	"PreviewEvaluationResource": "GET /api/v1/evaluation/resources/{resource_id}/preview",
	"SearchEvaluationTargets":   "GET /api/v1/evaluation/targets",
	"SaveEvaluationTarget":      "POST /api/v1/evaluation/targets",
	"GetEvaluationTarget":       "GET /api/v1/evaluation/targets/{target_id}",
	"ProbeEvaluationTarget":     "POST /api/v1/evaluation/targets/{target_id}/health-check",
	"StartEvaluationRun":        "POST /api/v1/evaluation/runs",
	"ListEvaluationJobs":        "GET /api/v1/jobs",
	"GetEvaluationJob":          "GET /api/v1/jobs/{job_id}",
	"CancelEvaluationJob":       "POST /api/v1/jobs/{job_id}/cancel",
	"RetryEvaluationJob":        "POST /api/v1/jobs/{job_id}/retry",
	"ResumeEvaluationJob":       "POST /api/v1/jobs/{job_id}/resume",
	"GetEvaluationJobRecovery":  "GET /api/v1/jobs/{job_id}/recovery",
	"GetEvaluationReport":       "GET /api/v1/evaluation/reports/{report_id}",
	"ExportEvaluationReport":    "GET /api/v1/evaluation/reports/{report_id}/export",
	"ListEvaluationEvidence":    "GET /api/v1/evaluation/evidence",
	"GetEvaluationEvidence":     "GET /api/v1/evaluation/evidence/{evidence_id}",
}

// skillGatewayRoutes registers every SkillGateway method against its upstream
// route.
var skillGatewayRoutes = map[string]string{
	"Enabled":      "exempt: pure local configuration check (no I/O)",
	"ListSkills":   "GET /api/v1/skills",
	"SearchSkills": "POST /api/v1/skills/search",
	"InstallSkill": "POST /api/v1/skills/install",
}

// runtimeGatewayRoutes registers every RuntimeGateway method against its
// upstream route. Note ListEvaluationJobs is intentionally shared with
// EvaluationGateway (same upstream endpoint, different concern).
var runtimeGatewayRoutes = map[string]string{
	"Enabled":                   "exempt: pure local configuration check (no I/O)",
	"ListRuntimeSessions":       "GET /api/v1/evaluation/sessions",
	"CreateRuntimeSession":      "POST /api/v1/evaluation/sessions",
	"GetRuntimeSession":         "GET /api/v1/evaluation/sessions/{session_id}",
	"DeleteRuntimeSession":      "DELETE /api/v1/evaluation/sessions/{session_id}",
	"ListRuntimeMessages":       "GET /api/v1/evaluation/sessions/{session_id}/messages",
	"PostRuntimeMessage":        "POST /api/v1/evaluation/sessions/{session_id}/messages",
	"ConfirmRuntimePlan":        "POST /api/v1/evaluation/sessions/{session_id}/confirm",
	"ListEvaluationJobs":        "GET /api/v1/jobs",
	"CancelRuntimeRun":          "POST /api/v1/instances/{instance_id}/runs/{run_id}/cancel",
	"StreamRuntimeRunEvents":    "GET /api/v1/instances/{instance_id}/runs/{run_id}/events",
	"CancelEvaluationRun":       "POST /api/v1/evaluation/runs/{run_id}/cancel",
	"StreamEvaluationRunEvents": "GET /api/v1/evaluation/runs/{run_id}/events",
}

// gatewayClientOwnRoutes registers the GatewayClient methods that are NOT part
// of the three embedded gateway interfaces (config + resource materialization).
// The embedded methods are covered by the three tables above; the merged
// GatewayClient contract is the union of all four tables.
var gatewayClientOwnRoutes = map[string]string{
	"GetRuntimeConfig":                "GET /api/v1/config",
	"UpdateRuntimeConfig":             "PUT /api/v1/config",
	"ValidateRuntimeConfig":           "POST /api/v1/config/validate",
	"TestRuntimeConfig":               "POST /api/v1/config/test",
	"RefreshRuntimeInstanceReadiness": "POST /api/v1/instances/{instance_id}/refresh-readiness",
	"MaterializeEvaluationResource":   "POST /api/v1/evaluation/resources/materialize",
}

// gatewayClientRoutes is the union contract for the composite GatewayClient
// interface (three embedded gateways + its own methods).
func gatewayClientRoutes() map[string]string {
	merged := make(map[string]string)
	for _, table := range []map[string]string{
		evaluationGatewayRoutes,
		skillGatewayRoutes,
		runtimeGatewayRoutes,
		gatewayClientOwnRoutes,
	} {
		for method, route := range table {
			merged[method] = route
		}
	}
	return merged
}

// methodSet returns the (flattened, deduplicated) method-name set of an
// interface type. Embedded interface methods are included by reflect.
func methodSet(iface reflect.Type) map[string]bool {
	set := make(map[string]bool, iface.NumMethod())
	for i := 0; i < iface.NumMethod(); i++ {
		set[iface.Method(i).Name] = true
	}
	return set
}

// contractIssues returns the bidirectional drift between an interface's method
// set and its registration table: a method with no entry, and an entry with no
// method. The slice is sorted for deterministic messages.
func contractIssues(iface reflect.Type, routes map[string]string) []string {
	actual := methodSet(iface)
	issues := make([]string, 0)
	for method := range actual {
		if _, ok := routes[method]; !ok {
			issues = append(issues, fmt.Sprintf(
				"interface method %q is not registered — please specify its HTTP route or an 'exempt: <reason>' entry", method))
		}
	}
	for method := range routes {
		if !actual[method] {
			issues = append(issues, fmt.Sprintf(
				"registration %q does not match any interface method (stale entry after a rename/removal?)", method))
		}
	}
	sort.Strings(issues)
	return issues
}

func interfaceType(v any) reflect.Type {
	return reflect.TypeOf(v).Elem()
}

// TestGatewayRouteContract asserts each gateway interface's method set equals
// its route/exemption registration set, in both directions.
func TestGatewayRouteContract(t *testing.T) {
	cases := []struct {
		name   string
		iface  reflect.Type
		routes map[string]string
	}{
		{"EvaluationGateway", interfaceType((*EvaluationGateway)(nil)), evaluationGatewayRoutes},
		{"SkillGateway", interfaceType((*SkillGateway)(nil)), skillGatewayRoutes},
		{"RuntimeGateway", interfaceType((*RuntimeGateway)(nil)), runtimeGatewayRoutes},
		{"GatewayClient", interfaceType((*GatewayClient)(nil)), gatewayClientRoutes()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if issues := contractIssues(tc.iface, tc.routes); len(issues) > 0 {
				t.Fatalf("%s contract drift:\n  - %s", tc.name, strings.Join(issues, "\n  - "))
			}
		})
	}
}

// TestGatewayRouteContractDetectsUnregisteredMethod proves the safety net is
// live: a fixture interface that adds a method absent from the registration
// table must be flagged. This holds the mechanism itself under test even when
// the real interfaces are clean.
func TestGatewayRouteContractDetectsUnregisteredMethod(t *testing.T) {
	type driftFixture interface {
		EvaluationGateway
		FakeNewMethod(context.Context) error
	}
	issues := contractIssues(interfaceType((*driftFixture)(nil)), evaluationGatewayRoutes)
	if len(issues) == 0 {
		t.Fatal("expected an issue for unregistered method FakeNewMethod, got none")
	}
	if joined := strings.Join(issues, "; "); !strings.Contains(joined, "FakeNewMethod") {
		t.Fatalf("expected issue to name FakeNewMethod, got: %s", joined)
	}
}

// TestGatewayRouteContractDetectsStaleRegistration covers the reverse
// direction: a registration whose method no longer exists must be flagged.
func TestGatewayRouteContractDetectsStaleRegistration(t *testing.T) {
	type narrowFixture interface {
		Enabled() bool
	}
	routes := map[string]string{
		"Enabled":       "exempt: pure local configuration check (no I/O)",
		"RemovedMethod": "GET /api/v1/gone",
	}
	issues := contractIssues(interfaceType((*narrowFixture)(nil)), routes)
	if len(issues) == 0 {
		t.Fatal("expected an issue for stale registration RemovedMethod, got none")
	}
	if joined := strings.Join(issues, "; "); !strings.Contains(joined, "RemovedMethod") {
		t.Fatalf("expected issue to name RemovedMethod, got: %s", joined)
	}
}
