# mailbox

Mailbox 领域仓。

本仓当前承载从 `nb-register` 迁入的 Outlook 相关代码。

## 目录

- `providers/outlook/imap-service`：Outlook Graph 邮件读取、邮箱账号存储、OTP 提取和 gRPC 服务。
- `providers/outlook/register-service`：Outlook 邮箱注册、OAuth 获取、邮箱存储同步和 gRPC 服务。
- `proto/email.proto`：邮件读取服务契约。
- `proto/mailbox_register.proto`：邮箱注册服务契约。
- `proto/mail_dns.proto`：邮箱 DNS 管理契约。

## 生成

```sh
sh scripts/generate-proto.sh
```

生成物用于本地检查和镜像构建，位于仓库忽略路径。

## 配置

`providers/outlook/register-service` 通过 `MAILBOX_EMAIL_SERVICE_ADDR` 连接邮箱存储服务；默认读取 `EMAIL_ADDR`，内置地址为 `outlook-imap-service:50051`。

## 检查

```sh
(cd providers/outlook/imap-service && go vet ./...)
python3 -m py_compile providers/outlook/register-service/*.py
```
