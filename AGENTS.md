# AGENTS.md

- 本仓承载 mailbox 领域代码；Outlook/Cloudflare provider adapter、邮箱注册、OAuth、收件和 webhook 能力统一位于 `services/mailbox-api`。
- Outlook 注册/OAuth 编排通过 `browser-automation` 执行浏览器步骤。
- Mailbox 对外 gRPC API 位于 `services/mailbox-api`，负责聚合邮箱注册、OAuth 和收件能力。
- 服务契约源文件位于 `proto/`；生成物由远程部署构建或 `scripts/generate-proto.sh` 生成。
- `webui/` 下手写前端源文件单文件不得超过 200 行；超过时先拆成业务模块、hook、utils，并复用 `webui` 仓 module-kit/uikit。
- `webui/` 前端实现必须尽量减少手写代码；优先使用官方组件、官方 SDK、官方示例推荐模式，或轻量组装 `webui` 暴露的官方组件后复用。
- `webui/` 下不得手写基础 UI 组件；toolbar、tabs、table/list、sheet/dialog、icon button、empty state、copy field 等统一从 `webui` 共享组件引入。
- `webui/` 表单优先使用 React Hook Form 结合 shadcn/Radix 官方表单、输入、选择和校验组件；本仓只声明业务字段、默认值、提交参数和轻量布局。
- `webui/` 只能依赖 `webui` 共享组件和本仓 mailbox 领域代码；不得要求 `webui` 仓 import 本仓页面源码，最终装载由 `deploy` 声明式组合完成。
- Outlook、Cloudflare 等 provider 必须在 proto、registry 或结构化配置中声明 capabilities/actions/required fields/required auth statuses/retention policy 等能力元数据；后端执行校验和前端渲染都基于这份声明。
- 邮箱资源项默认不重复携带 provider 已能声明的能力列表；邮箱项保留 provider、auth_status、password、refresh_token、access_token、domain 等状态和存储字段。
- 每个 provider 的页面组件必须拆到独立文件，例如 Outlook 和 Cloudflare 分开；公共 mailbox list/table/action 只做数据驱动复用，禁止把多个 provider UI 揉在一个大组件里。
- 每个 provider 的后端 adapter、收件策略、OAuth/webhook 能力、FIFO 保留策略和存储字段差异也必须在独立文件或明确 provider 边界内表达；公共服务只调度 provider adapter，不揉多个 provider 实现。
- provider 能力差异禁止散落硬编码 `if outlook/cloudflare`；需要差异时在 provider capability、provider adapter 或 provider 专属组件边界表达。
- 前端查询统一使用 TanStack Query；SSE/事件推送通过共享事件适配层进入 QueryClient cache 或本仓领域 hook。
- 第三方 provider 已有官方 SDK、官方 UI/Web Component、官方图标或官方设计资产时，优先使用官方维护包；不得手写等价组件或伪品牌图标。
- 不提交测试、CI/CD、运行日志、截图、token、cookie、refresh token、access token、注册结果或其他敏感运行产物。
- 后端优先使用 Go，按 Clean Code、DI 和面向抽象设计组织代码。
