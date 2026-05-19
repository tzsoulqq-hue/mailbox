# 贡献指南

## 边界

本仓当前维护 Outlook 邮箱读取、邮箱注册和 OAuth 相关运行单元。

## 开发流程

1. 修改契约时先改 `proto/`。
2. Go gRPC 代码通过各服务 Dockerfile 或 `scripts/generate-proto.sh` 生成。
3. 运行产物和敏感材料放在忽略路径。

## 验证

```sh
sh scripts/generate-proto.sh
(cd providers/outlook/imap-service && go vet ./...)
(cd services/mailbox-api && go vet ./...)
```
