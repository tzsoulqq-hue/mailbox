# mailbox

Mailbox 领域仓。

本仓当前承载从 `nb-register` 迁入的 Outlook 相关代码。

## 目录

- `providers/outlook/imap-service`：Outlook Graph 邮件读取、邮箱账号存储、OTP 提取和 gRPC 服务。
- `services/mailbox-api`：Mailbox 领域 gRPC API，内置邮箱注册导入、Outlook OAuth 编排和收件能力，通过 `browser-automation` 执行浏览器步骤。
- `proto/email.proto`：邮件读取服务契约。
- `proto/mailbox_register.proto`：邮箱注册与 OAuth 编排模型。
- `proto/mailbox_service.proto`：Mailbox 领域 API 契约。
- `proto/mail_dns.proto`：邮箱 DNS 管理契约。

## 生成

```sh
sh scripts/generate-proto.sh
```

生成物用于本地检查和镜像构建，位于仓库忽略路径。

## 配置

`services/mailbox-api` 的 workflow activity 负责邮箱注册导入、OAuth 结果获取和邮箱存储写入。Outlook OAuth 浏览器 profile 通过 `BROWSER_AUTOMATION_ADDR`、`OUTLOOK_REGISTER_AUTOMATION_PROXY_REF`、`OUTLOOK_REGISTER_AUTOMATION_LOCALE` 和 `OUTLOOK_REGISTER_AUTOMATION_TIMEZONE` 配置。

`services/mailbox-api` 通过 `MAILBOX_EMAIL_SERVICE_ADDR` 连接邮箱存储服务，并通过 `MAILBOX_API_PG_DSN` 维护邮箱操作状态投影。注册和 OAuth 流程由 mailbox-api 内置 Temporal worker 执行，使用 `TEMPORAL_ADDRESS`、`TEMPORAL_NAMESPACE`、`TEMPORAL_TASK_QUEUE` 和 `TEMPORAL_IDENTITY` 连接运行时。

## 检查

```sh
(cd providers/outlook/imap-service && go vet ./...)
(cd services/mailbox-api && go build ./...)
```
