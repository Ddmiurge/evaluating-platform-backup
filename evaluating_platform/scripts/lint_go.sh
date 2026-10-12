#!/usr/bin/env bash
#
# lint_go.sh — run golangci-lint for the evaluating_platform Go module and emit
# GitHub Actions annotations.
#
# ── Why this script exists instead of just calling golangci-lint ─────────────
# Two requirements that a bare `golangci-lint run` cannot satisfy:
#
#   1. FAIL-CLOSED. `golangci-lint run` exits 1 both when it found issues AND
#      (via exit code 3) when it crashed on a bad flag, an unloadable config,
#      or a compile error it could not type-check. A CI step that only looks at
#      "non-zero == fail" cannot tell "your code has warnings" from "the linter
#      never ran". Treating the latter as a pass is the classic silent-check
#      failure: the gate goes green while checking nothing.
#      This script classifies the exit code instead (see classify below).
#
#   2. ANNOTATIONS. golangci-lint v2.6.2 has NO `github-actions` output format
#      (verified against the shipped JSON schema: formats are checkstyle,
#      code-climate, html, json, junit-xml, sarif, tab, teamcity, text). So the
#      `::warning file=...,line=...::` workflow commands must be emitted here,
#      parsed from the JSON report so the file/line/col are exact rather than
#      re-parsed out of human-oriented text.
#
# ── Admission policy (phase 1 = warn, do not block) ─────────────────────────
# Issues are reported as `::warning` and the step succeeds. Rationale: the
# legacy backlog is large (see the job summary), and blocking on it today would
# stall every PR while tempting a mass "fix" that silences rather than solves.
# Phase 2 is to make NEW code blocking — NOT to grandfather existing debt
# forever. The intended switch is `--new-from-rev`, wired via LINT_NEW_FROM_REV
# below; do not reach for blanket exclusions to make the numbers look small.
#
# ── READING THE ISSUE COUNT (do not skip this) ─────────────────────────────
# The total printed by this script is a MEASUREMENT, and it is only comparable
# against another run of the SAME configuration. Before concluding "my change
# added N issues", read docs/lint-measurement.md. The two traps, abbreviated:
#
#   * `uniq-by-line` (now false in .golangci.yml) — golangci-lint's own dedup
#     merges same-text findings that land on the same LINE, while goconst
#     reports per OCCURRENCE. One gofmt'd blank line shifts every line number
#     below it, so unrelated findings collide and the total moves +/-1 with zero
#     change in code quality. Turning the flag OFF raised the baseline
#     184 -> 198 (all 14 in goconst). THAT RISE IS CORRECT, not a regression.
#   * Caching — golangci-lint v2 replays cached findings from os.UserCacheDir.
#     `setup-go`'s `cache: true` intentionally keeps it warm here (re-type-
#     checking the module is the slow part), which is fine for speed but means
#     the absolute count is only trustworthy on a cold cache. Never compare a
#     warm-cache run against a cold-cache run.
#
# The trustworthy signal is the PER-LINTER breakdown, not the total: a real
# regression moves one linter's count, while a line shift moves the total while
# every per-linter count stays put. docs/lint-measurement.md has a diff recipe
# that ignores line numbers entirely.
#
# Environment:
#   LINT_NEW_FROM_REV   Optional git rev. When set, only issues introduced after
#                       that rev are reported (phase-2 gate).
#   LINT_STRICT         Set to 1 to make findings fail the build (phase 2).
#   GOLANGCI_LINT_BIN   Override the binary (default: golangci-lint on PATH).
set -uo pipefail

readonly BIN="${GOLANGCI_LINT_BIN:-golangci-lint}"
readonly REPORT="${RUNNER_TEMP:-/tmp}/golangci-lint-report.json"
readonly TEXT_REPORT="${RUNNER_TEMP:-/tmp}/golangci-lint-report.txt"
readonly STRICT="${LINT_STRICT:-0}"

# Exit codes are a documented, stable part of golangci-lint's CLI contract
# (pkg/exitcodes/exitcodes.go). Verified empirically on v2.6.2, not assumed.
readonly EXIT_SUCCESS=0          # ran fine, no issues
readonly EXIT_ISSUES_FOUND=1    # ran fine, issues found
readonly EXIT_WARNING_IN_TEST=2 # ran fine, warning triggered from a test
readonly EXIT_FAILURE=3         # the tool itself failed (bad flag/config/crash)
readonly EXIT_TIMEOUT=4         # exceeded run.timeout
readonly EXIT_NO_GO_FILES=5     # nothing to analyze -> gate is meaningless
readonly EXIT_NO_CONFIG=6       # config file missing -> findings would be a lie
readonly EXIT_ERROR_LOGGED=7    # the linter logged an internal error

fail_closed() {
  echo "::error title=golangci-lint failed closed::${1}"
  exit 1
}

# ── Preflight: the tool must exist AND the config must load ──────────────────
command -v "$BIN" >/dev/null 2>&1 \
  || fail_closed "golangci-lint binary not found on PATH. The gate did NOT run; this is a failure, not a pass."
"$BIN" --version

# `config verify` validates against the shipped JSON schema. Without it, a typo
# in .golangci.yml silently degrades the gate (e.g. a misspelled linter name can
# drop a whole linter). Verified: v2 rejects unknown keys, so this is a real
# guard rather than a rubber stamp.
"$BIN" config verify \
  || fail_closed ".golangci.yml failed schema validation. Refusing to report 'no issues' from a config that would not load."

# ── Run the linter ───────────────────────────────────────────────────────────
lint_args=(
  run
  --timeout=10m
  # Emit JSON for annotation parsing; keep a human-readable copy for the log.
  --output.json.path="$REPORT"
  --output.text.path="$TEXT_REPORT"
)
# Phase 1: findings must not change the exit code, so that a real tool failure
# (3/4/5/6/7) stays distinguishable from "there are warnings" (1).
lint_args+=(--issues-exit-code=0)

if [[ -n "${LINT_NEW_FROM_REV:-}" ]]; then
  lint_args+=(--new-from-rev="$LINT_NEW_FROM_REV")
  echo "::notice title=golangci-lint phase-2 gate::Only issues newer than ${LINT_NEW_FROM_REV} are reported."
fi

set +e
"$BIN" "${lint_args[@]}" ./...
lint_status=$?
# NOTE: deliberately NOT `set -e` from here on. Every failure in this script is
# handled explicitly (fail_closed / the exit-code classification below), and
# turning errexit on mid-script would make an unrelated non-zero command abort
# the run with a bare exit code instead of our own diagnostic — which is how a
# "linter is broken" failure would masquerade as "linter found something".
set +e

# ── Classify: did the tool actually run? ─────────────────────────────────────
# Anything outside {0,1} means the gate produced no trustworthy verdict.
# This is the fail-closed core: never treat these as "clean".
case "$lint_status" in
  "$EXIT_SUCCESS"|"$EXIT_ISSUES_FOUND"|"$EXIT_WARNING_IN_TEST")
    echo "golangci-lint completed (exit ${lint_status})."
    ;;
  "$EXIT_FAILURE")
    fail_closed "golangci-lint exited ${EXIT_FAILURE} (tool failure — bad flag, unloadable config, or type-check error). Findings below, if any, are NOT trustworthy."
    ;;
  "$EXIT_TIMEOUT")
    fail_closed "golangci-lint timed out (exit ${EXIT_TIMEOUT}). An incomplete run must not be reported as a pass."
    ;;
  "$EXIT_NO_GO_FILES")
    fail_closed "golangci-lint found no Go files to analyze (exit ${EXIT_NO_GO_FILES}). A gate that inspected nothing must not pass."
    ;;
  "$EXIT_NO_CONFIG")
    fail_closed "golangci-lint could not find .golangci.yml (exit ${EXIT_NO_CONFIG}). Running with defaults would silently change which checks apply."
    ;;
  "$EXIT_ERROR_LOGGED")
    fail_closed "golangci-lint logged an internal error (exit ${EXIT_ERROR_LOGGED})."
    ;;
  *)
    fail_closed "golangci-lint returned unmapped exit code ${lint_status}. Treating as failure (fail-closed)."
    ;;
esac

# A missing/unparseable report is a failed gate, not an empty result set.
if [[ ! -s "$REPORT" ]]; then
  fail_closed "golangci-lint produced no JSON report at ${REPORT}. Cannot verify what was checked."
fi
if ! jq -e '.Issues | type == "array"' "$REPORT" >/dev/null 2>&1; then
  fail_closed "golangci-lint JSON report is malformed. Cannot verify what was checked."
fi

# ── Emit annotations + summary ──────────────────────────────────────────────
total="$(jq '.Issues | length' "$REPORT")"

if [[ "$total" -eq 0 ]]; then
  echo "✅ golangci-lint: 0 issues."
else
  echo "golangci-lint: ${total} issue(s). Reporting as warnings (phase 1: visibility, not blocking)."

  # Cap annotations: GitHub drops very large annotation sets, and 200+ warnings
  # on a PR is noise that hides the one that matters. The errcheck findings —
  # the audit class we actually care about — are sorted first so they can never
  # be crowded out by goconst noise.
  # The limit is applied INSIDE jq rather than with `| head -50`: truncating a
  # pipe makes jq die with "Broken pipe", which under `pipefail` would surface
  # as a bogus non-zero exit.
  jq -r '
    (.Issues
     | map(select(.FromLinter == "errcheck"))
     + map(select(.FromLinter != "errcheck"))
     | .[:50][]
     | "::warning file=\(.Pos.Filename),line=\(.Pos.Line),col=\(.Pos.Column),title=\(.FromLinter)::\(.Text | gsub("\n"; " "))"
    )' "$REPORT"

  if [[ "$total" -gt 50 ]]; then
    echo "::notice title=golangci-lint::${total} issues found; annotated the first 50 (errcheck prioritised). Full breakdown in the job summary."
  fi

  {
    echo "### golangci-lint — ${total} issue(s) (non-blocking)"
    echo ""
    echo "| linter | count |"
    echo "|---|---|"
    jq -r '.Issues | group_by(.FromLinter) | sort_by(-length)
           | .[] | "| \(.[0].FromLinter) | \(length) |"' "$REPORT"
    echo ""
    echo "<details><summary>Top 20 files by issue count</summary>"
    echo ""
    echo "| file | count |"
    echo "|---|---|"
    jq -r '.Issues | group_by(.Pos.Filename) | sort_by(-length) | .[:20]
           | .[] | "| \(.[0].Pos.Filename) | \(length) |"' "$REPORT"
    echo ""
    echo "</details>"
    if [[ "$STRICT" == "1" ]]; then
      echo ""
      echo "> **LINT_STRICT=1** — findings are blocking in this run."
    fi
  } >>"${GITHUB_STEP_SUMMARY:-/dev/null}"

  # ── Phase 2 gate: fail only on findings when explicitly enabled ───────────
  # Kept separate from the severity/exit-code plumbing above so that turning on
  # the strict gate cannot accidentally also mask a tool failure.
  if [[ "$STRICT" == "1" ]]; then
    echo "::error title=golangci-lint (strict)::${total} issue(s) and LINT_STRICT=1."
    exit 1
  fi
fi

echo "✅ golangci-lint gate passed (phase 1: warnings do not block)."