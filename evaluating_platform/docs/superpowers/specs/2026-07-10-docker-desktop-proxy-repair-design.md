# Docker Desktop 代理修复设计

## 背景与根因

企业门户消息请求能够到达 BFF 和 MaClawSrv，但 MaClawSrv 调用外部模型时返回 `EOF`，BFF 因此把 MaClaw 的 5xx 映射为 `maclaw runtime request failed`。

交叉验证表明 Docker Desktop 的宿主与容器代理仍处于手动模式，并指向已经无人监听的 `127.0.0.1:17890`；Windows 当前有效系统代理由 `mihomo.exe` 监听在 `127.0.0.1:8960`。宿主机直连或经 8960 访问模型端点可以完成 TLS，容器经 Docker 旧代理则在 TLS 握手阶段失败。

## 目标

- 把 Docker Desktop 切换为 System proxy，使其跟随当前 Windows 系统代理。
- 重启 Docker Desktop 后恢复平台容器出站 HTTPS。
- 验证 MaClawSrv 能访问模型端点，企业门户能够正常发送消息。
- 保持平台账号凭证、模型密钥、消息正文和评测载荷不被输出或写入诊断记录。

## 非目标

- 不修改 BFF、MaClawSrv 或前端业务代码。
- 不更换模型、模型密钥、租户配置或目标模型配置。
- 不把本机代理端口硬编码进仓库或 Compose。
- 不处理与本次故障无关的镜像部署漂移。

## 方案比较与决策

1. **System proxy（采用）**：修复 Docker Desktop 的根配置，让 engine 和容器跟随 Windows 当前代理；需要短暂重启 Docker，但避免下次系统代理端口变化时再次漂移。
2. 手工更新为 8960：可恢复当前连接，但端口再次变化后会复发。
3. 项目级 `HTTPS_PROXY=http://host.docker.internal:8960`：影响范围小，但把本机环境细节带入项目配置，且没有修复 Docker 的全局错误状态。

## 操作流程

1. 记录当前仅运行的平台容器与健康状态。
2. 通过 Docker Desktop 支持的设置界面把 Host/Container proxy 从旧手动配置切换为 System proxy；不直接手改运行时数据库或容器内部配置。
3. 应用设置并重启 Docker Desktop。
4. 等待 Docker engine 可用，并确认平台七个服务恢复健康；如 Compose 服务未自动恢复，则在项目目录执行 `docker compose up -d`。
5. 在 MaClawSrv 和 backend 容器中分别对模型端点发起不带凭证的 HTTPS 探针。预期完成 TLS 并返回 HTTP 401，而不是 curl 35/EOF。
6. 调用平台健康检查，并复核 MaClaw 日志没有新的 TLS EOF。
7. 使用企业门户现有会话发送一条普通消息，确认 BFF 不再返回 502；不得在日志或报告中复制消息正文。

## 回滚

如果 System proxy 不能恢复连接：

1. 不恢复已失效的 17890 配置。
2. 将 Docker Desktop 临时切换为手动 `127.0.0.1:8960`，重新应用并验证。
3. 若 Docker engine 本身无法恢复，保留数据卷，不执行删除、重置或清理操作，并报告实际状态。

## 验收标准

- Docker Desktop engine 正常运行。
- `ep_backend`、`ep_maclaw_runtime`、`ep_frontend`、`ep_hubcenter`、`ep_postgres`、`ep_redis`、`ep_minio` 均恢复健康。
- backend 与 MaClawSrv 容器访问模型端点能够完成 TLS；无凭证探针返回 HTTP 401。
- 企业门户发送普通消息不再返回 `maclaw_upstream_failed`/HTTP 502。
- Git 工作区除既有 `evaluating_platform/tmp_/` 和 `reports/` 外，不产生无关变更。
