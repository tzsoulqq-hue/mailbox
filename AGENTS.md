# AGENTS.md

- 本仓承载 mailbox 领域代码；当前只保留从 `byte-v-forge` 迁入的 Outlook provider 运行单元和相关 proto。
- Outlook Graph 邮件读取服务位于 `providers/outlook/imap-service`。
- Outlook 注册/OAuth 编排位于 `services/mailbox-api`，通过 `browser-automation` 执行浏览器步骤。
- Mailbox 对外 gRPC API 位于 `services/mailbox-api`，负责聚合邮箱注册、OAuth 和收件能力。
- 服务契约源文件位于 `proto/`；生成物由 Dockerfile 或 `scripts/generate-proto.sh` 在本地生成。
- 不提交测试、CI/CD、运行日志、截图、token、cookie、refresh token、access token、注册结果或其他敏感运行产物。
- 后端优先使用 Go，按 Clean Code、DI 和面向抽象设计组织代码。
