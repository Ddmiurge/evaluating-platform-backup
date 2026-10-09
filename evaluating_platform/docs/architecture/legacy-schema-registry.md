# Legacy Schema Registry (历史死表登记)

> 更新：2026-10-09（U1 起始批次，承接决策 **DD-8 = B：保留全部历史表，不执行 `DROP TABLE`**）
> 目的：登记数据库 schema 中**当前未被代码引用**的历史遗留表，避免新人误用；为将来可能的"数据退役计划"提供清单。
> 纪律：**本文件仅为登记与标注，不含任何 `DROP TABLE`**。退役/删除必须另起「数据退役计划」并经确认后执行。

## 背景

- 现有 `migrations/` 共 26 个迁移脚本、37 张表，**从未执行过 `DROP TABLE`**（`grep -rh "DROP TABLE" migrations/` 无输出）。
- 随 MaClawSrv 主路径收束，旧 Agent / 旧 Chat / 旧 Skill Runner / 旧 internal-external MCP / 旧编排 LLM 配置等链路的**运行代码已退役**（见 `legacy-retirement-plan.md`），但这些链路曾使用的表**作为历史遗留保留在 schema 中**。
- 以下 **11 张表**在非测试 Go 代码中引用数 = 0（复核命令见文末），判定为**当前未引用**。

## 登记清单（11 张）

| # | 表名 | 建表 migration | 原用途 | 当前状态 |
|---|---|---|---|---|
| 1 | `assessment_logs` | `001_init.sql:97` | 旧评估任务的**每次工具调用执行日志**（iteration / tool_name / input / output / thinking / tokens_used / duration_ms / severity），隶属旧 `assessments` 执行链路 | **当前未引用**（0 引用） |
| 2 | `target_systems` | `001_init.sql:31` | 旧"目标系统"注册表：被评估的 LLM/Agent 系统（type=openai/agent/custom、endpoint、api_key、config），旧 `assessments.target_id` 的外键目标 | **当前未引用**（0 引用） |
| 3 | `assets` | `001_init.sql:47` | 旧专家资产表（tool_config / workflow / suite；visibility、status、price_unit、call_count），旧 `assessments.template_id` 与 `billing_records.asset_id` 的引用目标 | **当前未引用**（`internal/` `cmd/` 中仅剩 2 处 `legacyGone` 410 退役路由 `GET /assets`、`POST /assets`，非真实表使用） |
| 4 | `skill_versions` | `013_skills.sql:23` | 旧 Skill **版本**表（manifest、package_object_path/hash/size、execution_runtime、entrypoint、self_test、validation_report、status），旧 Skill Runner 版本发布链路的载体 | **当前未引用**（0 引用） |
| 5 | `skill_runs` | `013_skills.sql:55` | 旧 Skill **运行记录**表（run_type=self_test/generate、stdout/stderr_log、result_payload、payload_dataset_summary、exit_code），旧 Skill Runner 执行与自检记录 | **当前未引用**（0 引用） |
| 6 | `external_mcp_servers` | `011_external_mcp_servers.sql:1` | 旧专家 **external MCP server** 注册表（base_url、transport_type、auth_*、timeout、status、last_sync），旧 externalmcp 子系统 | **当前未引用**（0 引用） |
| 7 | `external_mcp_tools` | `011_external_mcp_servers.sql:32` | 旧 external MCP **工具代理**表（remote_tool_name / proxy_tool_name / input_schema / capability_metadata），隶属 `external_mcp_servers` | **当前未引用**（0 引用） |
| 8 | `app_detectors` | `004_app_detector.sql:2` | 旧"应用检测器"表（sub_type=llm_detector、target_config、sample_ids/template_ids/package_ids），旧 detector 子系统 | **当前未引用**（0 引用） |
| 9 | `auxiliary_llm_configs` | `006_auxiliary_llm_configs.sql:5` | 旧**辅助 LLM** 按用户配置表（base_url / api_key / model），旧专家门户 AuxLLMConfig | **当前未引用**（0 引用） |
| 10 | `orchestration_llm_configs` | `007_llm_configs.sql:8` | 旧**编排 LLM** 按用户配置表（base_url / api_key / model），旧企业门户 OrchLLMConfig | **当前未引用**（0 引用） |
| 11 | `target_llm_configs` | `007_llm_configs.sql:28` | 旧**被测（目标）LLM** 按用户配置表（base_url / api_key / model / connector_type），旧企业门户 TargetLLMConfig；现由 `maclaw_target_configs`（平台加密 target config）取代 | **当前未引用**（0 引用） |

> ⚠️ **引用前必须先评估**：以上任何表在重新引入代码引用（读取/写入模型、新增 API、迁移脚本等）之前，**必须先评估**：它的语义是否已被 MaClawSrv 主路径 / `maclaw_*` 表取代、是否仍有历史数据依赖、以及是否会与 DD-8 的"保留不 drop"口径冲突。**不要**因为"表还在、字段齐全"就直接复用旧表——它们属于已退役链路。

## 相关替代关系（当前主路径）

| 旧表 | 当前主路径替代 |
|---|---|
| `target_systems` / `target_llm_configs` | `maclaw_target_configs`（平台加密 target config）+ 企业聊天页"被测对象连接" |
| `orchestration_llm_configs` | `maclaw_model_defaults`（平台默认模型）+ 账号级 provisioning 覆盖 |
| `auxiliary_llm_configs` | 管理员默认模型配置（`maclaw_model_defaults`） |
| `external_mcp_servers` / `external_mcp_tools` | MaClawSrv 原生 `manage_skill` + 专家 remote HTTP MCP（服务端管理，BFF 只回安全摘要） |
| `skill_versions` / `skill_runs` | 私有 Hub 发布链 + MaClawSrv 原生 Skill run | 
| `assets` / `assessment_logs` | 平台安全目录（sample/template/composed_attack）+ 红队 MCP bridge + `maclaw_redteam_reports` |

## 复核命令

```bash
cd evaluating_platform
# 逐表引用数（预期：除 assets 外均为 0；assets 的 2 处为 legacyGone 退役路由）
for t in assessment_logs target_systems assets skill_versions skill_runs \
         external_mcp_servers external_mcp_tools app_detectors \
         auxiliary_llm_configs orchestration_llm_configs target_llm_configs; do
  echo "$t : $(grep -rn "$t" --include='*.go' internal/ cmd/ | grep -v _test.go | wc -l)"
done
# 确认无 DROP TABLE（符合 DD-8=B）
grep -rh "DROP TABLE" migrations/ || echo "无 DROP（符合 DD-8=B）"
```
