# Architecture Docs

MaClawSrv 主路径文档。

| 文档 | 回答什么 |
|---|---|
| `development-status.md` | **到哪一步了、能做什么、还剩什么**（先看这份） |
| `maclaw-current-state.md` | 当前运行形态、数据流与安全边界 |
| `current-agent-workflow.md` | 企业聊天到确认执行、工具调用和报告的工作流 |
| `custom-plugin-publication.md` | 专家自定义插件 / 策略发布链路 |
| `strategy-code-packages.md` | **策略代码包设计稿**（可上传 JS、可执行；含租户隔离前置条件与实测清单） |
| `agent-target-preparation-guide.md` | 智能体目标接入 |
| `production-cutover-checklist.md` | 上线前配置、验证和回滚检查 |
| `legacy-retirement-plan.md` | 旧链路退役范围、tombstone 和保留项 |
| `legacy-schema-registry.md` | 历史 schema 登记 |
| `promptfoo-generation-options.md` | Plan A/Plan C 的历史设计记录（Plan A 已删除） |

「为什么这么做」在 `../../audit-2026-10-09/`，「上机跑成什么样」在 `../../records/`。
已删除迁移过程文档和路线图流水账。旧 Agent、旧 Skill Runner、旧 internal/external MCP、旧 CCBOS 执行路径和 QAgent 规划资料只通过 Git 历史追溯。
