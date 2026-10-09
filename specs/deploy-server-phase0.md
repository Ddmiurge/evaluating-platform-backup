# promptfoo 引擎 Phase 0 — 服务器部署验收手册

> 适用对象：远程主机（Linux + Docker）
> 简明五步：传文件夹 → 填 .env → compose up → 等健康 → 浏览器验收。
> 本机已验证：`go test ./...` 全绿；引擎 vitest 44/44 全绿（含真实 promptfoo 库 smoke e2e）；前端 `npm run build` 通过。

---

## 0. 前置条件

**需要传到服务器的只有一个文件夹：`evaluating_platform/`（自包含，compose/三个镜像构建源/迁移全在里面）。**

不需要传：仓库根目录的 `CC-BOS/`、`*-skill/`、`specs/`、`promptfoo_engine/`（旧位置已废弃）等；也不需要 `D:\promptfoo` 源码（引擎从 npm registry 拉锁定版本 0.123.0）。

```bash
docker --version && docker compose version   # Docker 20.10+ / compose v2
```

**maclaw 镜像检查**（compose 的 `maclaw-runtime` / `hubcenter` 用本地镜像名）：

```bash
docker images | grep -E 'maclaw-runtime|maclaw-hubcenter'
```

若没有，从原开发机导出：

```powershell
# 原开发机
docker save maclaw-runtime:latest maclaw-hubcenter:latest -o maclaw-images.tar
# 传到服务器后
docker load -i maclaw-images.tar
```

---

## 1. 传输与配置

```bash
scp -r evaluating_platform <user>@<server>:/opt/ep/     # 或 sftp/网盘，任选

cd /opt/ep/evaluating_platform
cp .env.example .env
vi .env
```

`.env` 必填三项：

| 变量 | 说明 |
|---|---|
| `LLM_API_KEY` | DeepSeek 等 OpenAI 兼容 API Key |
| `PROMPTFOO_ENGINE_BEARER_TOKEN` | ≥16 字符随机串（`openssl rand -hex 24`） |
| `JWT_SECRET` | 随机串（`openssl rand -hex 32`） |

---

## 2. 启动（首次约 10-20 分钟）

```bash
docker compose config --services
# 期望 9 行: postgres redis minio minio-init maclaw-runtime hubcenter backend frontend promptfoo-engine

docker compose up -d --build
```

说明：
- `promptfoo-engine` 从 registry 拉 `promptfoo@0.123.0`（Dockerfile 已内置 npmmirror 加速）。
- backend 容器启动自动执行 migration 026（`engine_runs` 表）。

```bash
watch -n5 'docker compose ps'    # 全部 (healthy) 后继续
```

---

## 3. 基础验收（5 分钟）

```bash
# 3.1 容器层
docker exec ep_backend curl -s http://promptfoo-engine:8090/health
# 期望: {"status":"ok",...}

docker exec ep_backend curl -s -o /dev/null -w '%{http_code}\n' http://promptfoo-engine:8090/runs/x
# 期望: 401 (无 token 必须拒绝)
```

```text
3.2 浏览器层
打开 http://<服务器IP>:5173
  → 登录页正常
  → 企业账号登录 → 聊天页「被测模型连接」配好 DeepSeek
  → 发起一次评测 → plan_confirm 卡 → 确认 → 进度卡 → PDF 报告   ← 既有主链路 OK
```

---

## 4. 引擎链路验收（Phase 0 核心，可选但建议做）

```bash
TOKEN="<企业账号 JWT>"   # 浏览器 F12 → Application → Local Storage 里取

# 4.1 引擎健康透传
curl -s -H "Authorization: Bearer $TOKEN" http://localhost:8080/api/v1/maclaw/engine/health
# 期望: {"status":"ok"}

# 4.2 发起引擎 run（此时目标模型已在上一步配好）
curl -s -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"purpose":"对客服模型做安全合规基线评估","plugins":[{"id":"harmful"},{"id":"pii"}],"strategies":[{"id":"jailbreak"}],"num_tests":3,"judge_mode":"auto"}' \
  http://localhost:8080/api/v1/maclaw/engine/runs
# 期望: 201 {"id":"er-...","phase":"queued",...}   ← 无任何 secret 字段

# 4.3 轮询到终态
RUN_ID=<上一步的 id>
curl -s -H "Authorization: Bearer $TOKEN" http://localhost:8080/api/v1/maclaw/engine/runs/$RUN_ID
# phase: queued → generating_tests → executing_probes → engine_judging → compiling_report → succeeded

# 4.4 红线断言
docker exec ep_postgres psql -U postgres -d evaluating_platform \
  -c "SELECT id, status, planned_count, executed_count FROM engine_runs ORDER BY created_at DESC LIMIT 5;"
# 人工抽查 result JSON：无 prompt 原文 / 目标响应原文 / sk- 开头 key / 容器路径
```

## 5. 常见问题

| 症状 | 处置 |
|---|---|
| 引擎反复重启，日志 `refuses to start` | compose 已默认注入四个 `PROMPTFOO_DISABLE_*=1`；检查 `.env` 是否覆盖 |
| 引擎构建拉包慢/失败 | Dockerfile 已内置 npmmirror；仍失败给 Docker 配代理 |
| backend 503 `engine_not_configured` | `.env` 确认 `PROMPTFOO_ENGINE_BASE_URL` 非空 |
| run 终态 `failed` | 多为被测目标不可达，先在聊天页做目标连通性检查 |
| `engine_runs` 表不存在 | `docker logs ep_backend` 看迁移日志，重启 backend |

## 6. Phase 0 验收判定

- [ ] 九容器全部 healthy
- [ ] 引擎健康 ok，无 token 401
- [ ] 端到端 run 到达 `succeeded`，result 为脱敏聚合
- [ ] `engine_runs` 无原文/凭据
- [ ] 既有 Skill 链路（聊天→报告）正常
