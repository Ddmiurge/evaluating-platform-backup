#!/usr/bin/env bash
# P28 · E-4 embedded-plugin batch build (deploy-host orchestrator).
#
# Runs on the host that holds the master key (.env + Docker + DB all local —
# same trust domain as the backend). The admin portal's 「请求构建」 button only
# writes a marker; THIS script is the deploy step that consumes it (D-10: the
# backend never touches the docker socket).
#
# Lifecycle (design §5 D1/D3):
#   -stage  → build engine image + backend image (the backend embeds the wizard
#             catalog) → recreate both → health check → -finalize -ok
#   any failure between stage and the health check → -finalize -fail -reason=…
#
# NO_QUEUE from -stage means there was no batch (no packages and no removals):
# deploy and finalize are both skipped — index.json exists ⟺ a batch awaits
# finalize, so calling -finalize after NO_QUEUE would hard-fail by design.
#
# Usage:
#   ./scripts/embedded-build.sh [--config configs/config.yaml] [--root <platform-dir>]
#                               [--skip-backend] [--timeout 900]
#
# --root is the PLATFORM directory (the one containing internal/ and
# promptfoo_engine/) — the same thing `go run ./cmd/embeddedbuild -root` takes.
# It defaults to this script's parent, NOT the repository root: record 54 §6.3
# showed the old default writing the catalog to <repo>/internal/... (ENOENT)
# and leaving a stray <repo>/promptfoo_engine/embedded-plugins/ tree behind.
#
# Exit codes: 0 success or NO_QUEUE; 1 stage failed; 2 build/deploy failed
# (the batch was marked build_failed); 3 health check failed (same); 4 finalize
# failed (the index is kept for a retry of -finalize).
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PLATFORM_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
ROOT="$PLATFORM_DIR"
CONFIG="configs/config.yaml"
TIMEOUT=900
SKIP_BACKEND=0

while [[ $# -gt 0 ]]; do
  case "$1" in
    --config) CONFIG="$2"; shift 2 ;;
    --root) ROOT="$2"; shift 2 ;;
    --timeout) TIMEOUT="$2"; shift 2 ;;
    --skip-backend) SKIP_BACKEND=1; shift ;;
    -h|--help) sed -n '2,/^set -euo pipefail$/p' "$0" | sed '$d'; exit 0 ;;
    *) echo "embedded-build: unknown argument: $1" >&2; exit 64 ;;
  esac
done

cd "$PLATFORM_DIR"

log() { printf 'embedded-build: %s\n' "$*"; }

# ── 1. stage ────────────────────────────────────────────────────────────────
log "stage: consuming build markers"
STAGE_OUT="$(go run ./cmd/embeddedbuild -stage -config "$CONFIG" -root "$ROOT")"
printf '%s\n' "$STAGE_OUT"
if grep -q '^NO_QUEUE$' <<<"$STAGE_OUT"; then
  log "no batch to build (NO_QUEUE) — deploy and finalize skipped"
  exit 0
fi

# From here on, a failure must mark the batch failed. The reason is a SAFE
# summary (no secrets, no payloads, no local paths) — it lands in the audit
# event and the admin portal.
mark_failed() {  local reason="$1"
  FINALIZED=1 # one finalize per invocation — the EXIT trap must not mark again
  log "FAILED: $reason — marking the batch build_failed"
  go run ./cmd/embeddedbuild -finalize -fail -reason="$reason" -config "$CONFIG" -root "$ROOT" \
    || log "WARNING: -finalize -fail itself failed; the batch stays build_requested for a manual retry"
}
trap 'code=$?; if [[ $code -ne 0 && -z "${FINALIZED:-}" ]]; then mark_failed "embedded-build.sh aborted at line ${LINENO} (exit ${code})"; fi' EXIT

# ── 2. build images ─────────────────────────────────────────────────────────
# Both images change in a batch: the engine COPYs the staged tree, and the
# backend embeds testdata/embedded_plugins.json (the wizard catalog source) at
# compile time. Rebuilding only one side shows a catalog whose ids the engine
# cannot run (or vice versa) — the "two views of one fact" failure this batch
# exists to prevent.
log "build: promptfoo-engine and backend"
if ! docker compose build promptfoo-engine || { [[ $SKIP_BACKEND -eq 0 ]] && ! docker compose build backend; }; then
  mark_failed "镜像构建失败"
  exit 2
fi

# ── 3. deploy ───────────────────────────────────────────────────────────────
log "deploy: recreating services"
docker compose up -d --no-deps --force-recreate promptfoo-engine || { mark_failed "引擎容器重建失败"; exit 2; }
if [[ $SKIP_BACKEND -eq 0 ]]; then
  docker compose up -d --no-deps --force-recreate backend || { mark_failed "后端容器重建失败"; exit 2; }
fi

# ── 4. health check (the deploy gate) ───────────────────────────────────────
# 「不绿不部署」: a batch is claimed baked only after the recreated services are
# actually healthy. Failures here mark the batch build_failed, never testing.
# Reads Docker's own healthcheck status (both services define one) so the host
# needs no in-container tool.
health_status() {
  docker inspect --format '{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}' "$1" 2>/dev/null || echo missing
}

wait_healthy() {
  local service="$1" container="$2"
  log "health: waiting up to ${TIMEOUT}s for ${service}"
  local deadline=$(( $(date +%s) + TIMEOUT ))
  while [[ $(date +%s) -lt $deadline ]]; do
    case "$(health_status "$container")" in
      healthy) return 0 ;;
      none) log "health: ${container} has no healthcheck; falling back to running state"
            [[ "$(docker inspect --format '{{.State.Running}}' "$container" 2>/dev/null)" == "true" ]] && return 0 ;;
    esac
    sleep 5
  done
  return 1
}

if ! wait_healthy promptfoo-engine ep_promptfoo_engine; then
  mark_failed "引擎健康检查超时（${TIMEOUT}s）"
  exit 3
fi

if [[ $SKIP_BACKEND -eq 0 ]]; then
  if ! wait_healthy backend ep_backend; then
    mark_failed "后端健康检查超时（${TIMEOUT}s）"
    exit 3
  fi
fi

# ── 5. finalize ─────────────────────────────────────────────────────────────
log "finalize: consuming this batch's index.json"
if ! go run ./cmd/embeddedbuild -finalize -ok -config "$CONFIG" -root "$ROOT"; then
  FINALIZED=1 # the index is kept on failure, so do NOT mark_failed over it
  log "finalize failed — the index was kept; re-run -finalize after fixing the cause"
  exit 4
fi
FINALIZED=1
log "batch deployed: rows build_requested → testing, remove_requested → removed"
