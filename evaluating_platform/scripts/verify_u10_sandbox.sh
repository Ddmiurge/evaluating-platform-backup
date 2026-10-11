#!/usr/bin/env bash
# verify_u10_sandbox.sh —— U10「沙箱边界」反证测试（写文件必须失败 / 联网必须失败）
#
# 用途：DoD 明确要求两项反证都做 ——
#   ① 尝试写文件 → 必须失败（只读根文件系统）
#   ② 尝试联网   → 必须失败（无外网出口）
# 本脚本把这两项做成**可在真实容器内运行**的探针，并区分三种结果：
#
#   PASS   —— 引擎容器内两项反证均成立（写失败 + 连不通）
#   FAIL   —— 引擎容器内任一反证被推翻（写成功 或 连得上）
#   SKIP   —— 本机无 Docker，**未实测**。这不是 PASS，退出码 2。
#
# ⚠️ 退出码语义（勿混用）：
#   0 = PASS（实测反证通过）
#   1 = FAIL（实测反证被推翻 —— 沙箱声明与实际不符）
#   2 = SKIP（环境不具备，未实测 —— 必须在报告中如实登记）
#
# 为什么必须区分 SKIP 与 PASS：本批在开发机上无 Docker，若把「没跑」写成
# 「通过」，CI 快照就会把一个未验证的安全声明固化成绿灯。U6/U7/U8 三批的
# 共同教训是「未验证的声明等于没有声明」，所以这里让「没跑」在退出码上可见。
#
# 用法：
#   ./scripts/verify_u10_sandbox.sh              # 自动检测 docker，可用则实测
#   CONTAINER=ep_promptfoo_engine ./scripts/verify_u10_sandbox.sh
#   SKIP_IF_NO_DOCKER=0 ./scripts/verify_u10_sandbox.sh   # 无 docker 时直接失败
#
# 可覆盖：CONTAINER / SKIP_IF_NO_DOCKER / PROBE_HOST / PROBE_PORT

set -uo pipefail

readonly CONTAINER="${CONTAINER:-ep_promptfoo_engine}"
readonly PROBE_HOST="${PROBE_HOST:-api.promptfoo.app}"
readonly PROBE_PORT="${PROBE_PORT:-443}"
readonly SKIP_IF_NO_DOCKER="${SKIP_IF_NO_DOCKER:-1}"

info() { printf '\033[36m[INFO]\033[0m %s\n' "$*"; }
ok()   { printf '\033[32m[ OK ]\033[0m %s\n' "$*"; }
bad()  { printf '\033[31m[FAIL]\033[0m %s\n' "$*"; }
skip() { printf '\033[33m[SKIP]\033[0m %s\n' "$*"; }
die()  { printf '\033[31m[ERROR]\033[0m %s\n' "$*" >&2; exit 2; }

command -v docker >/dev/null 2>&1 || {
  if [[ "${SKIP_IF_NO_DOCKER}" == "1" ]]; then
    skip "本机无 docker —— 沙箱反证**未实测**（需要 CI 或具备 Docker 的环境运行本脚本）。"
    skip "声明核对请另行查看：promptfoo_engine/Dockerfile、docker-compose.yml、promptfoo_engine/src/sandbox.ts"
    exit 2
  fi
  die "本机无 docker，且 SKIP_IF_NO_DOCKER=0 —— 按要求视为失败。"
}

docker inspect "${CONTAINER}" >/dev/null 2>&1 || {
  skip "容器 ${CONTAINER} 不存在（引擎未运行）—— 沙箱反证**未实测**。"
  skip "启动方式：docker compose up -d promptfoo-engine"
  exit 2
}

info "容器：$(docker inspect -f '{{.Name}} user={{.Config.User}} readonly={{.HostConfig.ReadonlyRootfs}}' "${CONTAINER}")"

# ── 反证 ①：写文件必须失败 ────────────────────────────────────────────────────
# 写 /etc —— 在 read_only:true 下必然失败；在非 root 下同样失败（/etc 属 root）。
# 两个条件都满足才算「只读根 + 非 root」同时成立，因此写成功即推翻声明。
info "反证 ①：尝试写文件（期望失败）…"
if docker exec -u 0 "${CONTAINER}" sh -c 'echo u10 > /etc/u10-sandbox-probe' >/dev/null 2>&1; then
  bad "写 /etc 成功 —— 只读根文件系统声明被推翻（且容器可被切到 root 执行）"
  docker exec -u 0 "${CONTAINER}" sh -c 'rm -f /etc/u10-sandbox-probe' >/dev/null 2>&1 || true
  WRITE_VERDICT=failed
else
  ok "写 /etc 失败 —— 只读根文件系统按预期生效"
  WRITE_VERDICT=blocked
fi

# ── 反证 ②：联网必须失败 ──────────────────────────────────────────────────────
# 目标是 promptfoo 自己的远程生成服务：能连上就说明
# PROMPTFOO_DISABLE_REMOTE_GENERATION=1 只在库层面生效，网络层面没阻断。
info "反证 ②：尝试联网 ${PROBE_HOST}:${PROBE_PORT}（期望失败）…"
if docker exec "${CONTAINER}" node -e "
  const net = require('node:net');
  const s = new net.Socket();
  s.setTimeout(3000);
  s.once('connect', () => { s.destroy(); process.exit(0); });
  s.once('timeout', () => { s.destroy(); process.exit(1); });
  s.once('error', () => { s.destroy(); process.exit(1); });
  s.connect(${PROBE_PORT}, '${PROBE_HOST}');
" >/dev/null 2>&1; then
  bad "成功连接 ${PROBE_HOST}:${PROBE_PORT} —— 无外网声明被推翻"
  NET_VERDICT=reachable
else
  ok "连接 ${PROBE_HOST}:${PROBE_PORT} 失败 —— 无外网出口按预期生效"
  NET_VERDICT=blocked
fi

# ── 引擎自身的沙箱报告（区分 verified / declared / probed）────────────────────
info "引擎沙箱报告（GET /sandbox，需 bearer token）…"
docker exec "${CONTAINER}" sh -c 'wget -qO- --header="Authorization: Bearer ${PROMPTFOO_ENGINE_BEARER_TOKEN}" http://127.0.0.1:8090/sandbox' 2>/dev/null || \
  skip "无法读取 /sandbox（容器内 wget 不支持 header 或服务未就绪）—— 不影响上面两项反证的结论"

echo
if [[ "${WRITE_VERDICT}" == "blocked" && "${NET_VERDICT}" == "blocked" ]]; then
  ok "U10 沙箱反证实测通过：写文件被拒 + 外网不可达"
  exit 0
fi
bad "U10 沙箱反证实测失败：write=${WRITE_VERDICT} network=${NET_VERDICT}"
exit 1