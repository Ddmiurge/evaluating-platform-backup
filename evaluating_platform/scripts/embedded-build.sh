#!/usr/bin/env bash
# P28 · E-4 embedded-plugin batch build (deploy-host orchestrator).
#
# Runs on the host that holds the master key (.env + Docker + DB all local —
# same trust domain as the backend). The admin portal's 「请求构建」 button only
# writes a marker; THIS script is the deploy step that consumes it (D-10: the
# backend never touches the docker socket).
#
# Lifecycle (design §5 D1/D3), two deploy modes:
#
#   default (code changed):
#     -stage → build engine+backend images → recreate both → health check
#            → -finalize -ok
#
#   --skip-build (E-4d hot path, zero-downtime plugin batch):
#     -stage → SIGHUP both loaders (engine registry reload + backend catalog
#            refresh; NO build, NO recreate — containers keep their uptime)
#            → poll /health receipts until engine embedded_batch == this batch
#              AND backend embedded_catalog_hash == sha256(staged file)
#            → -finalize -ok   (「同步了再显示」: finalize only after both sides
#              acknowledge; a busy engine is retried on the next poll, a
#              malformed marker fails the batch without touching state)
#
#   any failure before the gate → -finalize -fail -reason=…
#
# Use --skip-build ONLY when engine/backend CODE is unchanged; a code change
# still needs the default build (recreate/HUP cannot ship new code). Skipping
# the build when code changed ships no fix, which is why the default stays
# build-everything. NOTE (§14.6): SIGHUP kills an OLD image (no handler) —
# upgrade images with a default (build) run BEFORE using the hot path.
#
# --skip-backend only skips the backend IMAGE build/recreate (build path). The
# hot path ALWAYS HUPs and polls the backend: the wizard catalog is the other
# half of the batch, and syncing only the engine would publish the "two views
# of one fact" failure the design forbids (§14.5-5 双回执).
#
# NO_QUEUE from -stage means there was no batch (no packages and no removals):
# deploy and finalize are both skipped — batch.json exists ⟺ a batch awaits
# finalize, so calling -finalize after NO_QUEUE would hard-fail by design.
# (E-4c, records/57 §6: index.json is the PERSISTENT engine catalog — stage
# rewrites it, finalize NEVER deletes it; only the batch marker moves.)
#
# Usage:
#   ./scripts/embedded-build.sh [--config configs/config.yaml] [--root <platform-dir>]
#                               [--skip-backend] [--skip-build] [--timeout 900] [--min-free-gb 10]
#
# --skip-build = E-4d hot path: SIGHUP + receipt poll instead of build/recreate.
#
# --root is the PLATFORM directory (the one containing internal/ and
# promptfoo_engine/) — the same thing `go run ./cmd/embeddedbuild -root` takes.
# It defaults to this script's parent, NOT the repository root: record 54 §6.3
# showed the old default writing the catalog to <repo>/internal/... (ENOENT)
# and leaving a stray <repo>/promptfoo_engine/embedded-plugins/ tree behind.
#
# Exit codes: 0 success or NO_QUEUE; 1 stage failed; 2 build/deploy failed
# (the batch was marked build_failed); 3 health check failed (same); 4 finalize
# failed (the index is kept for a retry of -finalize); 5 host preflight failed
# (Go missing / Docker storage below --min-free-gb — nothing was staged).
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PLATFORM_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
ROOT="$PLATFORM_DIR"
CONFIG="configs/config.yaml"
TIMEOUT=900
SKIP_BACKEND=0
SKIP_BUILD=0
MIN_FREE_GB=10

while [[ $# -gt 0 ]]; do
  case "$1" in
    --config) CONFIG="$2"; shift 2 ;;
    --root) ROOT="$2"; shift 2 ;;
    --timeout) TIMEOUT="$2"; shift 2 ;;
    --min-free-gb) MIN_FREE_GB="$2"; shift 2 ;;
    --skip-backend) SKIP_BACKEND=1; shift ;;
    --skip-build) SKIP_BUILD=1; shift ;;
    -h|--help) sed -n '2,/^set -euo pipefail$/p' "$0" | sed '$d'; exit 0 ;;
    *) echo "embedded-build: unknown argument: $1" >&2; exit 64 ;;
  esac
done

cd "$PLATFORM_DIR"

log() { printf 'embedded-build: %s\n' "$*"; }

# Preflight: the batch tool runs on THIS host (it needs the host's platform
# tree and config to reach the DB). A host without Go fails later with a
# confusing "go: command not found" from inside a $(...) substitution
# (record 55 §8) — say it up front, before stage can write anything.
if ! command -v go >/dev/null 2>&1; then
  log "ERROR: 'go' is required on this host to run cmd/embeddedbuild; install Go (>=1.22) or run this script on the build host"
  exit 64
fi

# Disk preflight: a full Docker root kills a build mid-COPY (record 54 §3:
# ResourceExhausted after /var hit 100%, 70GB of builder cache). Fail BEFORE
# stage so no batch marker is touched and the operator can prune first.
case "$MIN_FREE_GB" in
  ''|*[!0-9]*) log "ERROR: --min-free-gb must be a non-negative integer"; exit 64 ;;
esac
if ! docker info >/dev/null 2>&1; then
  log "ERROR: the Docker daemon is not reachable on this host"
  exit 5
fi
docker_root="$(docker info --format '{{.DockerRootDir}}' 2>/dev/null || true)"
[[ -n "$docker_root" ]] || docker_root="/var/lib/docker"
while [[ ! -d "$docker_root" && "$docker_root" != "/" ]]; do docker_root="$(dirname "$docker_root")"; done
free_kb="$(df -Pk "$docker_root" | awk 'NR==2 {print $4}')"
if [[ -z "$free_kb" || "$free_kb" -lt $((MIN_FREE_GB * 1024 * 1024)) ]]; then
  log "ERROR: Docker storage ${docker_root} has $(( ${free_kb:-0} / 1024 / 1024 )) GB free; --min-free-gb=${MIN_FREE_GB} required. Run 'docker builder prune -af' first."
  exit 5
fi
log "preflight: go ok; ${docker_root} has $(( free_kb / 1024 / 1024 )) GB free (min ${MIN_FREE_GB})"

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

# Batch id from the stage line (`STAGED <batch> packages=N removed=M`) — the
# value both /health receipts must echo back before finalize (§14.5-5).
BATCH_ID="$(awk '/^STAGED /{print $2; exit}' <<<"$STAGE_OUT")"
if [[ -z "$BATCH_ID" ]]; then
  mark_failed "stage 输出缺少批次号"
  exit 1
fi

# ── 2+3+4 (hot path): SIGHUP → poll receipts ───────────────────────────────
# The receipt contract (design §14.3): engine /health carries embedded_batch
# (+ the last reload reason), backend /api/v1/health carries the content hash
# of the catalog snapshot it is serving. Static strings only.
engine_receipt() {
  # Three lines: batch, reload reason, reload ok. node exists in the engine
  # image; fetch is global since Node 18.
  docker exec "$1" node -e '
    const port = process.env.PROMPTFOO_ENGINE_PORT || "8090";
    fetch("http://127.0.0.1:" + port + "/health")
      .then((r) => r.json())
      .then((j) => {
        console.log(String((j && j.embedded_batch) || ""));
        console.log(String((j && j.embedded_reload && j.embedded_reload.reason) || ""));
        console.log(String((j && j.embedded_reload && j.embedded_reload.ok) || ""));
      })
      .catch(() => process.exit(1));
  ' 2>/dev/null
}

backend_hash() {
  # curl ships in the backend image; SERVER_PORT defaults to 8080 in config.
  docker exec ep_backend sh -c 'curl -fsS "http://127.0.0.1:${SERVER_PORT:-8080}/api/v1/health"' 2>/dev/null \
    | sed -n 's/.*"embedded_catalog_hash":"\([0-9a-f]*\)".*/\1/p'
}

sha256_file() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  else
    shasum -a 256 "$1" | awk '{print $1}'
  fi
}

p8_running() {
  docker compose ps --status running --services 2>/dev/null | grep -qx 'promptfoo-engine-p8test'
}

sync_hot_batch() {
  local expect_hash="$1"
  local deadline=$(( $(date +%s) + TIMEOUT ))
  local last_reason=""
  while [[ $(date +%s) -lt $deadline ]]; do
    # Re-send every iteration: a busy engine refuses the reload (single worker
    # with a run in flight) and must be retried once the run finishes — the
    # design's 忙拒 → 重试 loop lives here (§14.5-3).
    docker kill -s HUP ep_promptfoo_engine >/dev/null 2>&1 || true
    if p8_running; then
      docker kill -s HUP ep_promptfoo_engine_p8test >/dev/null 2>&1 || true
    fi
    # Backend ALWAYS (see header): a half-synced batch is the two-views
    # failure; --skip-backend does not opt out of the receipt.
    docker kill -s HUP ep_backend >/dev/null 2>&1 || true
    sleep 3

    local out e_batch e_reason e_ok
    if out="$(engine_receipt ep_promptfoo_engine)"; then
      e_batch="$(sed -n 1p <<<"$out")"
      e_reason="$(sed -n 2p <<<"$out")"
      e_ok="$(sed -n 3p <<<"$out")"
      case "$e_reason" in
        malformed_index|missing_index|not_patched|reload_threw)
          # Fail fast with the static reason — retrying a bad marker cannot
          # succeed, and finalize must record build_failed (§14.5-2).
          REASON="引擎目录重载失败（${e_reason}）"
          return 1 ;;
      esac
      local backend_ok=1
      local got_hash
      got_hash="$(backend_hash)"
      if [[ "$got_hash" != "$expect_hash" ]]; then
        backend_ok=0
      fi
      # A RUNNING dedicated engine is part of the batch too (57 §6 附带): its
      # receipt must echo the same batch or it would keep serving the previous
      # catalog while everything else moved on.
      local p8_ok=1 p8_out=""
      if p8_running; then
        if p8_out="$(engine_receipt ep_promptfoo_engine_p8test)" && [[ "$(sed -n 1p <<<"$p8_out")" == "$BATCH_ID" ]]; then
          p8_ok=1
        else
          p8_ok=0
        fi
      fi
      if [[ "$e_batch" == "$BATCH_ID" && $backend_ok -eq 1 && $p8_ok -eq 1 && "$e_ok" != "false" ]]; then
        log "sync ok: engine batch=${e_batch}; backend hash=${expect_hash}; p8test=${p8_ok}"
        return 0
      fi
      last_reason="engine_batch=${e_batch:-none} backend_sync=${backend_ok} p8test_sync=${p8_ok} reason=${e_reason:-none}"
    else
      last_reason="engine health unreadable"
    fi
    log "sync: waiting (${last_reason})"
  done
  REASON="热加载回执超时（${last_reason:-unknown}）"
  return 1
}

REASON=""
if [[ $SKIP_BUILD -eq 1 ]]; then
  # ── E-4d zero-downtime batch: no build, no recreate ──
  EXPECT_HASH="$(sha256_file "$ROOT/internal/maclaw/testdata/embedded_plugins.json")"
  log "hot: HUP reload for ${BATCH_ID} (no build, no recreate; expect hash ${EXPECT_HASH})"
  if ! sync_hot_batch "$EXPECT_HASH"; then
    mark_failed "${REASON:-热加载同步失败}"
    exit 3
  fi
else
  # ── 2. build images ─────────────────────────────────────────────────────
  # Both images change in a batch: the engine COPYs the staged tree, and the
  # backend embeds testdata/embedded_plugins.json (the wizard catalog source)
  # at compile time. Rebuilding only one side shows a catalog whose ids the
  # engine cannot run (or vice versa) — the "two views of one fact" failure
  # this batch exists to prevent.
  log "build: promptfoo-engine and backend"
  if ! docker compose build promptfoo-engine || { [[ $SKIP_BACKEND -eq 0 ]] && ! docker compose build backend; }; then
    mark_failed "镜像构建失败"
    exit 2
  fi

  # ── 3. deploy ───────────────────────────────────────────────────────────
  # Recreate reloads the persistent catalog from the volume mounts (E-4c: the
  # index is no longer deleted at finalize, so every restart keeps the full
  # catalog). Multi-engine (records/57 §6 附带): a RUNNING dedicated engine must
  # be recreated too or it keeps serving the previous catalog; a stopped one is
  # left stopped (the operator's state, and it serves nobody).
  log "deploy: recreating services"
  docker compose up -d --no-deps --force-recreate promptfoo-engine || { mark_failed "引擎容器重建失败"; exit 2; }
  if p8_running; then
    docker compose up -d --no-deps --force-recreate promptfoo-engine-p8test || { mark_failed "专属引擎容器重建失败"; exit 2; }
  fi
  if [[ $SKIP_BACKEND -eq 0 ]]; then
    docker compose up -d --no-deps --force-recreate backend || { mark_failed "后端容器重建失败"; exit 2; }
  fi

  # ── 4. health check (the deploy gate) ───────────────────────────────────
  # 「不绿不部署」: a batch is claimed baked only after the recreated services
  # are actually healthy. Failures here mark the batch build_failed, never
  # testing. Reads Docker's own healthcheck status (both services define one)
  # so the host needs no in-container tool.
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

  if p8_running; then
    if ! wait_healthy promptfoo-engine-p8test ep_promptfoo_engine_p8test; then
      mark_failed "专属引擎健康检查超时（${TIMEOUT}s）"
      exit 3
    fi
  fi

  if [[ $SKIP_BACKEND -eq 0 ]]; then
    if ! wait_healthy backend ep_backend; then
      mark_failed "后端健康检查超时（${TIMEOUT}s）"
      exit 3
    fi
  fi
fi

# ── 5. finalize ─────────────────────────────────────────────────────────────
log "finalize: consuming this batch's batch.json (both receipts matched)"
if ! go run ./cmd/embeddedbuild -finalize -ok -config "$CONFIG" -root "$ROOT"; then
  FINALIZED=1 # the batch marker is kept on failure, so do NOT mark_failed over it
  log "finalize failed — the batch marker was kept; re-run -finalize after fixing the cause"
  exit 4
fi
FINALIZED=1
log "batch deployed: rows build_requested → testing, remove_requested → removed"
