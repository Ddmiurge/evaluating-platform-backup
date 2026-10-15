# Legacy Retirement Record

更新时间：2026-05-22

## 结论

旧受控 Agent、旧 Skill Runner、旧 internal/external MCP 和旧 CCBOS 执行链路已经从运行代码中退役。当前仓库不保留旧源码副本，回滚依赖 Git 历史、镜像版本和数据备份。

## 已删除主体

- `internal/agent`
- `internal/chat`
- `internal/mcptools`
- `internal/skill`
- `internal/externalmcp`
- `internal/report`
- `internal/hub`
- `internal/connector`
- `internal/detector`
- `cmd/skillrunner`
- `Dockerfile.skillrunner`
- **Plan A 生成链路**（`promptfoo_engine/src/runner/generateTests.ts`，P3-3，2026-10-15）
  以及选择它的开关 `PROMPTFOO_ENGINE_NATIVE_GENERATION`

旧 `skill_runner`、`ccbos_mcp` compose service 和相关镜像不属于当前启动图。

### Plan A 退役说明（P3-3）

Plan A 是「平台自己的生成 LLM 合成攻击提示词 + promptfoo 只做 evaluate 执行」那条链路。
它被删除的原因不是它坏，而是它**重复**：promptfoo 原生生成（Plan C）已经拥有生成范式
（插件模板、实体提取、策略变换、判定器），而两条链路在相同输入下产出的探针数量不同、
报告外观却一样 —— 用户会并列比较两份不可比的报告（U7 item14）。

删除范围：`generateTests.ts` 与其测试、`redteamRun.ts` 的路径选择与 Plan A 专属拒绝、
`env.ts` 的开关与 `effectiveWorkerCount`、`server.ts` 的条件分支、前端与文档里的开关说明。

**不可配置级回滚**：开关已删除，回滚手段从「翻一个变量」变为「回退镜像版本」（见
`production-cutover-checklist.md` 的回滚策略）。历史报告里的 `generation_path=plan_a`
保留渲染，因为那些报告仍需要能解释自己。

前端也已从挂载源码的开发容器收口为可部署镜像：主 compose 不再运行 `npm run dev`，而是构建 Vite 静态资源并由 nginx 托管。

该 nginx 镜像仍是 BFF 入口代理的一部分：`/api/v1` 代理需要保留至少 650s 的读写发送超时，避免 MaClaw confirmed run 长请求被前端代理截断成 504。

## 已退役前端入口

- 旧企业 assessment 执行页。
- 旧企业 Skill open 页。
- 旧专家 SkillManager。
- 旧专家 ExternalMCPManager。
- 旧 `/chat/*` 调用路径。
- 旧 `/skills*`、`/external-mcp-servers*` service 方法。
- 旧企业 `OrchLLMConfig` / `TargetLLMConfig`。
- 旧专家 `AuxLLMConfig`。

当前前端入口是企业聊天工作台、专家数据/Skill/MCP 页面和管理员治理门户。

## Tombstone API

以下旧 API 保留最小 `410 Gone`：

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

响应只能包含安全错误码和迁移说明，不返回 secret、payload、内部路径、MaClaw credential 或 evidence content。

## 不再支持的配置

- `maclaw.enabled=false`
- `skill.legacy_apis_enabled`
- `SKILL_LEGACY_APIS_ENABLED`
- `SKILL_RUNNER_*`
- `CCBOS_*`
- 旧 `agent` / `mcp` / `skill.runner_*` 配置段
- 全局 `maclaw.api_token` / `maclaw.default_instance_id` 生产 fallback

当前只支持 MaClawSrv 账号级 provisioning 和 BFF 适配层。

## 保留项

暂不删除：

- 历史 migration。
- 历史 assessment/report/resource/skill 表。
- 登录、权限、计费、门户、历史展示仍引用的数据结构。

如需 drop 表、清历史数据或迁移历史记录，应另起数据退役计划。

## 替代路径

- 智能体编排和评测执行：MaClawSrv。
- 专家 Skill：私有 Hub + MaClaw 原生 Skill。
- 专家样本/模板/已组合攻击：平台安全目录 + 红队 MCP bridge 粗粒度工具。
- 企业 target 调用：平台加密 target config + `call_evaluation_target`。
- 证据和报告：`save_redteam_evidence` + `compile_redteam_report` 固定中文 PDF。
- CCBOS：普通 Hub Skill `ccbos-classical-chinese-skill`，不再是旧 MCP/Skill Runner 路径。

## 验证基线

```powershell
cd C:\Users\wangboyang\Desktop\evaluating_platform\evaluating_platform
go test ./...

cd C:\Users\wangboyang\Desktop\evaluating_platform\evaluating_platform\frontend
npm run build

cd C:\Users\wangboyang\Desktop\evaluating_platform
git diff --check
```

额外验收：

- `docker compose config --services` 不包含 `skill_runner`、`ccbos_mcp`。
- tombstone API 返回 `410 Gone`。
- 企业主链路通过 BFF 创建 session、确认执行、读取 job/SSE/report/export。
- 响应扫描不包含 token、credential、target secret、payload、archive、evidence content 或本地路径。
