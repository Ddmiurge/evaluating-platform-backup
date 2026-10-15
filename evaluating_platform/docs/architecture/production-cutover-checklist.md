# Production Cutover Checklist

更新时间：2026-05-22

## 目标状态

生产路径：

```text
frontend -> evaluating_platform backend BFF -> MaClawSrv
```

`evaluating_platform` 不直接执行评测，不启动旧 Agent、旧 internal MCP、旧 Skill Runner 或 external MCP/CCBOS 服务。

## 启动服务

```powershell
cd C:\Users\wangboyang\Desktop\evaluating_platform\evaluating_platform
docker compose up -d postgres redis minio minio-init maclaw-runtime hubcenter backend frontend
```

开发时如果要从本地完整 MaClawSrv 源码构建：

```powershell
docker compose -f docker-compose.yml -f docker-compose.local-maclaw.yml up -d --build maclaw-runtime hubcenter backend frontend
```

如果要连接外部 MaClawSrv，不启动 compose 内的 `maclaw-runtime`：

```powershell
$env:MACLAW_BASE_URL="http://host.docker.internal:18080"
docker compose -f docker-compose.yml -f docker-compose.external-maclaw.yml up -d --build backend frontend
```

服务清单应只包含：

- `postgres`
- `redis`
- `minio`
- `minio-init`
- `maclaw-runtime`
- `hubcenter`
- `backend`
- `frontend`

验证：

```powershell
docker compose config --services
```

Frontend must be a deployable image, not a Vite dev container:

- `frontend/Dockerfile` builds static assets with Node and serves them with nginx.
- `/api/v1/*` is proxied by nginx to `backend:8080`.
- The compose service exposes `5173:80` for local access.
- The service must not mount `./frontend` or run `npm run dev` in the production compose file.

## 必需配置

Backend：

- `MACLAW_RUNTIME_KIND=maclawsrv`
- `MACLAW_BASE_URL`
- `MACLAW_RUNTIME_IMAGE`
- `MACLAW_HUBCENTER_IMAGE`
- `MACLAW_ADMIN_SECRET`
- `MACLAW_PROVISIONING_ENABLED=true`
- `MACLAW_REDTEAM_MCP_ENDPOINT`
- `MACLAW_REDTEAM_MCP_SECRET`
- `MACLAW_REDTEAM_TARGET_CONCURRENCY=5`
- `MACLAW_TIMEOUT_SECONDS>=180`
- `MACLAW_HTTP_WRITE_TIMEOUT_SECONDS>=600`
- `CRYPTO_MASTER_KEY`
- `JWT_SECRET`
- `MINIO_ACCESS_KEY`
- `MINIO_SECRET_KEY`

MaClawSrv：

- `MACLAW_DATA_ROOT=/data/maclaw`
- `MACLAW_HTTP_ADDR=0.0.0.0:18080`
- `MACLAW_ADMIN_SECRET`
- `MACLAW_TOKEN_SECRET`
- `MACLAW_CREDENTIAL_PEPPER`
- `MACLAW_SECURITY_POLICY_MODE`

本地开发可用 `MACLAW_SECURITY_POLICY_MODE=developer`；生产前必须换成受信包策略。

不得使用：

- `MACLAW_ENABLED`
- `SKILL_LEGACY_APIS_ENABLED`
- `SKILL_RUNNER_*`
- `CCBOS_*`

HubCenter compose expectations:
- service name: `hubcenter`
- backend/internal URL: `http://hubcenter:9388`
- host debug URL: `http://localhost:9388`
- health endpoint: `/healthz`
- persistent volume: `hubcenter_data`

## 管理员配置验收

- 管理员账号可登录。
- 模型配置可保存平台默认 provider，并完成 validate/test。
- 保存默认模型后，已 provision 租户同步成功；新账号首次访问后继承默认配置。
- Hub 管理配置统一私有 Hub URL，默认只允许 `skillhub` 来源。
- Hub sync/status 可看到所有已 provision 租户的同步状态。
- 用户与租户页面显示每个账号的 MaClaw tenant/user/instance/readiness。
- 资源治理只显示专家发布目录安全摘要，不显示 payload。
- 页面不回显 raw API key、Hub secret、MaClaw token、credential 或 target secret。

## 多租户验收

- 新建两个专家账号和一个企业账号。
- 三个账号对应不同 MaClaw tenant/user/instance。
- 专家 A/B 只能看到各自数据、Skill 和 MCP server。
- 企业账号能看到统一专家已发布安全目录。
- 企业使用专家数据时，只在确认执行后准备选中的 ref。
- BFF 响应不包含 payload、credential secret、MaClaw token、admin secret 或 evidence content。

## Skill / Hub 验收

- 专家上传 Skill ZIP 后，BFF 发布到配置的私有 Hub。
- Hub 返回 `skill_id` 后，Skill 安装到专家 tenant。
- 已发布 Skill 同步安装到 ready 企业 tenant。
- 企业侧 MaClaw 通过原生 `manage_skill` 搜索和运行 Skill。
- CCBOS 作为普通 Skill 验收，不走旧 CCBOS MCP 或 BFF 专用流程；管理员模型配置必须已同步到执行租户，CCBOS 运行时必须走 `tenant_llm_ccbos`，并能消费 MaClaw 传入的专家样本/问题。
- 选中 Skill 的计划确认前，BFF 应快速预检当前租户 MaClaw 模型配置；模型不可达时返回安全明确错误，不进入长时间 confirmed run。
- 单个 stale 企业映射不得导致专家上传整体失败；失败项进入 Hub/status 后续处理。

## 专家数据和 MCP 验收

- 专家可上传样本、模板、已组合攻击。
- 模板上传支持 `.xlsx` / CSV 三列：`序号 / 分类名称 / 模板内容`。
- 模板分类支持内置八类和自定义分类。
- 专家可配置 remote HTTP MCP server。
- 专家 MCP 响应只显示安全摘要、header 名称和 `has_auth_secret`，不回显 secret 或 env 值。
- 企业侧 MaClaw 可通过平台 MCP bridge 搜索专家数据/资源/MCP 摘要。

## 主链路验收

- 企业配置被测模型连接，target health 通过。
- 企业聊天中“你好，你能干什么”只返回安全评估工作台范围内能力说明。
- 模糊需求返回追问或探索，不生成执行卡；`ask_user` 在前端显示为普通问题和选项按钮，不显示 JSON。
- 信息完整后返回 `plan_confirm`，包含目标摘要、风险类型、建议测试轮次、选择策略、选中能力和选择理由。
- 点击确认时请求携带 `plan_message_id` 和用户最终确认的 `test_count`；BFF 按指定计划卡确认并只准备选中能力。
- 点击确认后原计划卡保持“已确认”，下面追加进度卡并创建 `evaluation.run`。
- MaClaw 确认后优先调用 `execute_redteam_evaluation_batch`，由平台按 `MACLAW_REDTEAM_TARGET_CONCURRENCY` 并发执行 payload 组合、target 调用、攻击判定、证据保存和中文报告；判定应先解析 `judge_profile`，仅对越狱/文言文等攻击 profile 快速处理实质非拒答成功，提示注入、合规和通用安全 profile 使用各自 rubric，模糊结果再使用小批量并发 LLM 判定，避免 20 轮以上评测被单个超大判定请求拖慢；旧单步工具只用于兼容/调试。
- Skill-backed confirmed runs call native `manage_skill(action="run")`, register the resulting `payload_dataset` through `register_skill_payload_dataset`, and pass those `payload_handles` into `execute_redteam_evaluation_batch`; missing Skill handles must fail clearly instead of falling back to raw samples.
- 图文多模态 Skill-backed runs may register `payload_text + images[]` payloads. When the enterprise target metadata has `supports_vision=true`, target calls use OpenAI-compatible text+image message content; when the target is text-only, the run returns safe `target_multimodal_not_supported` failures without sending image payloads. Browser/report/progress outputs must expose only safe image metadata, never raw base64 or image URLs containing secrets.
- FigStep、MM-SafetyBench、HADES 多模态 Skill 应通过私有 Hub 导入专家门户，并能被企业侧 Skill list/search 发现；小规模自检只验证 Skill 输出与 handle 注册，不对当前被测模型做大规模图文攻击调用。
- job progress、SSE、完成卡、report、PDF export 可通过 BFF 访问。
- 响应和 localStorage 不包含 secret、payload、完整目标响应、archive、evidence content 或本地路径。

## 旧 API 验收

以下路径返回 `410 Gone`：

- `/api/v1/chat/*`
- `/api/v1/skills*`
- `/api/v1/enterprise/skills/:id/launch`
- `/api/v1/external-mcp-servers*`
- `/api/v1/assessments*`
- `/api/v1/tools`
- `/api/v1/tools/schemas`
- `/api/v1/assets`
- `/api/v1/tools/categories`
- `/api/v1/orchestration-llm/*`
- `/api/v1/target-llm/*`
- `/api/v1/auxiliary-llm/*`

## 回滚策略

旧执行主体已删除，不支持配置级回落旧链路。

**P3-3 起额外一条**：`PROMPTFOO_ENGINE_NATIVE_GENERATION` 开关已删除，因此「把生成路径
翻回 Plan A」这条回滚**不再存在** —— 计划生成只剩 promptfoo 原生一条。回滚手段相应变为
只能回退镜像/commit（下面第一条）。

可用回滚方式：

- 回滚到上一 Git commit 或镜像版本。
- 保留 MaClaw 数据 volume，不随应用回滚删除。
- backend/frontend 问题优先回滚对应镜像。
- MaClawSrv 问题按 MaClaw 自身版本和数据兼容策略处理。
- 历史数据库表暂不 drop，避免回滚时因 schema 缺失失败。

## 自动化验证

```powershell
cd C:\Users\wangboyang\Desktop\evaluating_platform\evaluating_platform
go test ./...

cd C:\Users\wangboyang\Desktop\evaluating_platform\evaluating_platform\frontend
npm run build

cd C:\Users\wangboyang\Desktop\evaluating_platform
git diff --check
```
