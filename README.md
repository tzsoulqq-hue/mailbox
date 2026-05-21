# mailbox

Mailbox 领域仓，承载邮箱账号、Outlook provider、邮箱注册和 OAuth workflow。

## 目录

- `services/mailbox-api`：Mailbox 领域 gRPC API 和唯一服务进程，内置 Outlook/Cloudflare provider adapter、邮箱注册导入、Outlook OAuth 编排、收件、webhook 和邮件信号解析能力。
- `Dockerfile`：部署入口，只构建并启动 mailbox API 一个进程。
- `workers/cloudflare-email-relay`：Cloudflare Email Routing Worker，将 CF 入站邮件转发到 mailbox webhook。
- `proto/email.proto`：邮件读取服务契约。
- `proto/mailbox_register.proto`：邮箱注册与 OAuth 编排模型。
- `proto/mailbox_service.proto`：Mailbox 领域 API 契约。
- `proto/mail_dns.proto`：邮箱 DNS 管理契约。

## 生成

```sh
sh scripts/generate-proto.sh
```

生成物用于远程构建和部署验证，位于仓库忽略路径。

## 配置

`services/mailbox-api` 的 workflow activity 负责邮箱注册导入、OAuth 结果获取和邮箱存储写入。Outlook OAuth 浏览器 profile 通过 `BROWSER_AUTOMATION_ADDR`、`OUTLOOK_REGISTER_AUTOMATION_PROXY_REF`、`OUTLOOK_REGISTER_AUTOMATION_LOCALE` 和 `OUTLOOK_REGISTER_AUTOMATION_TIMEZONE` 配置。

`services/mailbox-api` 直接内置 Outlook 和 Cloudflare provider adapter，并通过 `MAILBOX_PG_DSN` 维护邮箱、邮件和操作状态投影。注册和 OAuth 流程由 mailbox workflow worker 执行，使用 `WORKFLOW_RUNTIME_ADDRESS`、`WORKFLOW_RUNTIME_NAMESPACE`、`WORKFLOW_RUNTIME_TASK_QUEUE` 和 `WORKFLOW_RUNTIME_IDENTITY` 连接运行时。

Outlook 邮件读取使用 Microsoft Graph Go SDK，默认读取当前 OAuth 用户的 messages，并用 `Prefer: outlook.body-content-type="text"` 请求文本正文；只有显式覆盖 `OUTLOOK_GRAPH_MESSAGES_URL` 时才走兼容 REST adapter。

Cloudflare 邮件是主动推送链路：Email Routing Worker 收到邮件后把标准化事件 POST 到 `/webhooks/email/cloudflare`，mailbox 服务使用 `MAILBOX_WEBHOOK_HTTP_ADDR` 开启 HTTP webhook，并只通过 `X-Webhook-Token` 读取 `MAILBOX_WEBHOOK_TOKEN` 校验转发请求。Outlook Graph webhook 使用 `/webhooks/email/microsoft-graph`，验证 URL 必须带同一个 token，POST 通知的 `clientState` 也必须等于同一个 token。Cloudflare 域名池来自 Cloudflare API：`MAILBOX_CLOUDFLARE_API_TOKEN` 读取 `MAILBOX_CLOUDFLARE_EMAIL_CONFIG_FILE` 中声明的 zones，并从 Email Routing catch-all 规则与 Cloudflare MX DNS 记录推导可用邮箱域名；token 限制到目标 zone，并授予 `Zone Read`、`DNS Read` 和 `Email Routing Rules Read` 即可。Cloudflare 地址不需要手动导入，邮件到达后按 recipient 自动形成虚拟邮箱并按 domain 分组展示。需要公网入口时在 deploy 的 `ingress.webhook` 暴露 mailbox webhook，或使用受管 HTTPS 隧道把该入口映射到公网域名；业务代码不管理公网隧道。

邮件保留策略按 provider 独立执行 FIFO：Outlook 使用 `MAILBOX_OUTLOOK_MAX_MESSAGES_PER_MAILBOX` 限定每个邮箱的最大邮件数，Cloudflare 使用 `MAILBOX_CLOUDFLARE_MAX_MESSAGES_PER_DOMAIN` 限定每个 domain 的最大邮件数。超过上限时删除最早邮件及对应 seen 记录，默认分别为 `100` 和 `500`。

邮件内容会先落库，再通过 mailbox 通用解析器生成 `EmailSignal`。通用解析器只识别验证码等可复用邮件信号；业务状态判断由业务服务通过 webhook 或查询读取邮件后自行完成。

实时下游通知使用 `MAILBOX_OUTBOUND_WEBHOOKS_FILE` 指向结构化配置文件。文件内容使用 `proto/mailbox_service.proto` 里的 `OutboundEmailWebhookList` proto JSON，过滤规则参考 Cloudflare Email Workers 暴露的 envelope sender、envelope recipient、headers/subject 和 raw body 能力，只保留必要字段：provider、recipient email/domain、sender email/domain、subject keyword 和 signal kind。domain 规则同时匹配自身和子域，例如 `openai.com` 会匹配 `tm.openai.com`。默认不发送正文，只有 `include_body` 为 true 时才发送截断后的文本正文；认证 token 通过 `token_env` 指向环境变量，不写进配置文件。

示例：

```json
{
  "webhooks": [
    {
      "name": "downstream-service",
      "url": "http://downstream-service:8080/webhooks/mailbox/email",
      "tokenEnv": "MAILBOX_DOWNSTREAM_WEBHOOK_TOKEN",
      "filter": {
        "providers": ["MAILBOX_PROVIDER_CLOUDFLARE", "MAILBOX_PROVIDER_OUTLOOK"],
        "recipientDomains": ["example.invalid"],
        "signalKinds": ["EMAIL_SIGNAL_KIND_OTP"]
      },
      "previewLimit": 500
    }
  ]
}
```

## 检查

```sh
cd ../deploy
./scripts/deploy-remote.sh mailbox
```

业务构建、镜像构建和部署验证统一在远程宿主机执行，本机只做源码编辑和调度。
