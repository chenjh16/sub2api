# spec 分支相对 main 的变更说明

本文档说明当前 `spec` 分支相对 `main` 主线的功能性修改。记录时点为 2026-06-12，本地功能对比范围如下；本文档提交本身仅用于说明，不计入功能差异：

- 基线：`main` / `e34ad2b1`
- 功能分支：`spec` / 当前 HEAD
- 对比范围：分组默认 `service_tier`、OpenAI 上游结构化错误 failover、自动故障转移策略、200 响应内容拦截，以及对应文档与测试。

## 总览

`spec` 分支围绕 OpenAI 网关稳定性和 Codex Team 使用体验做了四类增强：

1. 为 OpenAI 分组增加默认 `service_tier`，支持在分组级别自动给请求注入 `priority`、`flex` 等服务层级。
2. 识别上游结构化限流/冷却错误，避免把此类错误原样返回给客户端，改为触发账号 failover 并临时暂停该账号调度。
3. 在管理后台“网关服务”增加自动故障转移策略，支持配置结构化 `400`、连续 `5xx`、连续网络错误的短冷却。
4. 在管理后台“网关服务”增加 200 响应内容关键词拦截，用于处理上游把维护、繁忙、换 Key 等文案伪装成成功响应的情况。

这四项修改均优先遵循已有网关调度和 failover 机制，不新增独立调度器。

## 1. OpenAI 分组默认 service_tier

### 目标

部分 OpenAI 分组，例如 Codex Team，希望默认启用 OpenAI API 的 fast/priority 模式，但又不希望要求每个下游客户端都显式传入 `service_tier`。本分支新增分组级默认值：

- 当请求没有显式传入 `service_tier` 时，由网关自动补上分组默认值。
- 当客户端已经传入 `service_tier` 时，尊重客户端选择，不强制覆盖。
- `fast` 别名仍按既有逻辑规范化为 `priority`。

### 数据库与模型

新增迁移：

```text
backend/migrations/150_add_group_openai_default_service_tier.sql
```

新增字段：

```sql
ALTER TABLE groups
    ADD COLUMN IF NOT EXISTS openai_default_service_tier varchar(20) NOT NULL DEFAULT '';
```

字段约束：

```text
''、priority、flex、auto、default、scale
```

涉及 Ent 与领域模型：

- `backend/ent/schema/group.go`
- `backend/ent/group.go`
- `backend/ent/group_create.go`
- `backend/ent/group_update.go`
- `backend/internal/service/group.go`
- `backend/internal/handler/dto/types.go`

### 后端行为

新增 helper：

- `normalizeOpenAIGroupDefaultServiceTier`
- `openAIGroupDefaultServiceTier`
- `applyOpenAIGroupDefaultServiceTierToBody`
- `applyOpenAIGroupDefaultServiceTierToWSResponseCreate`

生效路径：

- OpenAI `/v1/responses`
- OpenAI `/v1/chat/completions`
- OpenAI 兼容 `/v1/messages`
- Realtime / WebSocket `response.create`
- WS v2 passthrough adapter

注入规则：

```text
if group.platform == "openai"
   and group.openai_default_service_tier != ""
   and request body does not contain service_tier:
       inject service_tier = group.openai_default_service_tier
```

若请求体已经存在 `service_tier`，网关不会用分组默认值覆盖它。

### 管理后台

在分组创建和编辑页面的 OpenAI 配置区域增加“默认 service_tier”下拉框。

可选项：

- 关闭
- `priority (fast)`
- `flex`
- `auto`
- `default`
- `scale`

相关文件：

- `frontend/src/views/admin/GroupsView.vue`
- `frontend/src/types/index.ts`
- `frontend/src/i18n/locales/zh.ts`
- `frontend/src/i18n/locales/en.ts`

### Codex Team 推荐配置

如果需要给名为 `Codex Team` 的 OpenAI 分组默认开启 fast 模式，可设置：

```text
openai_default_service_tier = priority
```

也可直接通过 SQL 配置：

```sql
UPDATE groups
SET openai_default_service_tier = 'priority'
WHERE name = 'Codex Team'
  AND platform = 'openai';
```

### 兼容性

- 新字段默认值为空，不会改变现有分组行为。
- 只对 OpenAI 平台分组生效。
- 非 OpenAI 分组切换或保存时，前端会清空该字段。
- 客户端显式传入 `flex`、`default`、`auto` 等值时，仍按客户端值转发。

专项文档见：

```text
docs/openai-default-service-tier.md
```

## 2. 结构化限流/冷却错误自动 failover

### 背景

部分上游会返回结构化错误，例如冷却类错误：

```json
{
  "error": {
    "type": "invalid_request_error",
    "code": "rate_limit_cooldown"
  },
  "code": "rate_limit_cooldown",
  "limit_type": "cooldown"
}
```

也可能返回 RPM 限流类错误：

```json
{
  "error": {
    "type": "invalid_request_error",
    "code": "rate_limit_exceeded"
  },
  "code": "rate_limit_exceeded",
  "limit_type": "rpm"
}
```

这类响应虽然可能是 HTTP 400，但实际语义是上游账号、上游公益 Key 或上游节点临时不可用。如果原样转发给 Agent 客户端，客户端无法自动切换其他账号，体验会变成显式失败。

### 识别策略

新增结构化识别函数：

```go
isOpenAIUpstreamCooldownFailoverError(statusCode, upstreamBody)
isOpenAIUpstreamRateLimitExceededFailoverError(statusCode, upstreamBody)
```

共同前置条件：

- HTTP 状态码为 `400 Bad Request`
- 响应体是合法 JSON

冷却类命中条件满足任一字段：

- `error.code == "rate_limit_cooldown"`
- 顶层 `code == "rate_limit_cooldown"`
- 顶层 `limit_type == "cooldown"`

RPM 限流类命中条件必须同时满足：

- `error.code == "rate_limit_exceeded"` 或顶层 `code == "rate_limit_exceeded"`
- 顶层 `limit_type == "rpm"` 或 `error.limit_type == "rpm"`

该策略只识别结构化字段，不依赖中文或英文 message 文案。

### failover 行为

命中后：

1. `shouldFailoverOpenAIUpstreamResponse` 返回 true。
2. 当前上游错误进入既有 `UpstreamFailoverError` 路径。
3. `handleOpenAIAccountUpstreamError` 对当前 OpenAI 账号设置运行时暂停调度。
4. 暂停时长默认为 10 分钟。
5. handler 层继续按已有逻辑选择其他可用账号重试。

运行时暂停原因：

```text
rate_limit_cooldown
rate_limit_exceeded_rpm
```

### 影响范围

主要作用于 OpenAI 网关 HTTP 错误响应处理路径，包括 Responses、Chat Completions、Messages、Embeddings、Images 等复用 `shouldFailoverOpenAIUpstreamResponse` 或 `handleOpenAIAccountUpstreamError` 的转发路径。

### 兼容性

- 只改变结构化 `rate_limit_cooldown`、`rate_limit_exceeded + limit_type=rpm` 的处理方式。
- 不会根据 message 中的“十分钟”等自然语言进行硬编码判断。
- 只有 `rate_limit_exceeded` 但没有 `limit_type=rpm` 的 HTTP 400 响应仍走原有错误处理规则。
- 如果上游返回其他 400 错误，仍走原有错误处理规则。

### 测试覆盖

新增和更新测试覆盖：

- 结构化 `rate_limit_cooldown` 被识别为 failover。
- 结构化 `rate_limit_exceeded + limit_type=rpm` 被识别为 failover。
- 命中后账号运行时调度暂停约 10 分钟。
- Codex CLI only 相关路径不会因为该逻辑产生回归。

相关测试文件：

- `backend/internal/service/openai_account_runtime_block_fastpath_test.go`
- `backend/internal/service/openai_gateway_service_codex_cli_only_test.go`
- `backend/internal/service/openai_upstream_rate_limit_failover_test.go`

专项文档见：

```text
docs/openai-upstream-error-failover.md
```

## 3. 自动故障转移策略

### 背景

原有网关已经支持 `UpstreamFailoverError`：当 service 层返回该错误且下游尚未收到响应内容时，handler 层会选择其他可用账号继续重试。因此“一个通道失败自动冷却并切到下一个”的核心能力已经存在。

本分支在该机制上补齐了更细的 OpenAI 策略：

- 结构化 `400` 限流/冷却错误默认进入 failover，并对当前账号运行时冷却。
- HTTP `5xx` 单次仍立即 failover；同一账号连续达到阈值后，运行时短冷却。
- 瞬时网络/传输错误默认返回 `UpstreamFailoverError`；同一账号连续达到阈值后，运行时短冷却。
- 明确持久的代理认证、DNS、连接拒绝等错误仍沿用已有 DB 临时禁用逻辑。

### 配置入口

管理后台路径：

```text
设置 -> 网关服务 -> 自动故障转移策略
```

系统设置键：

```text
gateway_failover_policy_settings
```

Admin API：

```http
GET /api/v1/admin/settings/gateway-failover-policy
PUT /api/v1/admin/settings/gateway-failover-policy
```

默认配置：

| 字段 | 默认值 | 范围 | 说明 |
| --- | --- | --- | --- |
| `structured_400_enabled` | `true` | true/false | 是否启用结构化 `400` 限流/冷却 failover |
| `structured_400_cooldown_minutes` | `10` | 1-720 | 结构化 `400` 命中后当前账号运行时冷却时长 |
| `failure_cooldown_jitter_percent` | `20` | 0-100 | 连续失败短冷却的随机抖动比例 |
| `http_5xx_cooldown_enabled` | `true` | true/false | 是否对连续 HTTP `5xx` 启用短冷却 |
| `http_5xx_threshold` | `3` | 1-20 | 窗口内多少次 `5xx` 后触发短冷却 |
| `http_5xx_window_seconds` | `30` | 1-3600 | `5xx` 连续失败统计窗口 |
| `http_5xx_cooldown_seconds` | `120` | 1-7200 | `5xx` 达阈值后的运行时冷却时长 |
| `transport_cooldown_enabled` | `true` | true/false | 是否对连续瞬时网络/传输失败启用短冷却 |
| `transport_threshold` | `3` | 1-20 | 窗口内多少次瞬时网络/传输失败后触发短冷却 |
| `transport_window_seconds` | `30` | 1-3600 | 瞬时网络/传输失败统计窗口 |
| `transport_cooldown_seconds` | `120` | 1-7200 | 瞬时网络/传输失败达阈值后的运行时冷却时长 |

### 实现细节

新增设置模型：

- `GatewayFailoverPolicySettings`
- `DefaultGatewayFailoverPolicySettings`
- `GetGatewayFailoverPolicySettings`
- `GetGatewayFailoverPolicySettingsCached`
- `SetGatewayFailoverPolicySettings`

OpenAI service 新增进程内连续失败计数器：

```text
openaiConsecutiveFailureCounters
```

计数类别：

```text
http_5xx
transport
```

触发短冷却时调用：

```go
BlockAccountScheduling(account, until, reason)
```

原因：

```text
http_5xx_threshold
transport_threshold
```

成功收到非错误 HTTP 响应后，网关会清除该账号的连续失败计数，避免把非连续故障累计成冷却。

### 接入范围

新增策略接入了 OpenAI 主要 HTTP 转发路径：

- `/v1/responses`
- `/v1/chat/completions`
- 兼容 `/v1/messages`
- Responses 转 Chat Completions fallback
- Embeddings
- Images
- Images Responses
- passthrough 转发路径

原先部分兼容端点在 transport 错误时会直接写 `502`，本分支改为返回 `UpstreamFailoverError`，交给 handler 层统一切换账号。

### 边界

- 不把所有 `400` 都当作 failover；只处理结构化限流/冷却字段。
- 不根据自然语言 `message` 猜测冷却时间。
- 连续失败短冷却是进程内状态，多实例部署时各实例独立统计。
- 已经向下游写出流式内容后，不能无痕切换账号。
- `429`、`529`、鉴权错误、模型不存在等仍沿用既有持久状态处理。

## 4. 200 响应内容关键词拦截

### 背景

还有一类上游不会返回 HTTP 错误，而是把维护、繁忙、换 Key、公告等内容伪装成成功响应。例如：

```text
当前繁忙，休息十分钟，tg频道：https://t.me/UniverseFederation
```

或：

```text
公益服务器压力很大，休息十分钟换key开放...
```

如果这类内容出现在 HTTP 200 或 SSE 正常输出中，原有网关会认为模型调用成功，并把它当作模型文本发给下游 Agent。HTTP 状态码级 failover 无法覆盖这种情况。

### 配置入口

管理后台路径：

```text
管理后台 -> 设置 -> 网关服务 -> 200 响应内容拦截
```

新增配置项：

| 字段 | 默认值 | 范围 | 说明 |
| --- | --- | --- | --- |
| `enabled` | `false` | true/false | 是否启用内容拦截 |
| `keywords` | `[]` | 最多 100 条 | 每行一个关键词，命中任意一条即拦截 |
| `cooldown_minutes` | `10` | 1-720 | 命中后当前账号暂停调度时长 |
| `max_scan_bytes` | `65536` | 1024-1048576 | 每个响应最多扫描的前缀字节数 |

系统设置键：

```text
gateway_content_blocker_settings
```

Admin API：

```http
GET /api/v1/admin/settings/gateway-content-blocker
PUT /api/v1/admin/settings/gateway-content-blocker
```

请求和响应结构：

```json
{
  "enabled": true,
  "keywords": ["当前繁忙，休息十分钟", "公益服务器压力很大", "站点维护中"],
  "cooldown_minutes": 10,
  "max_scan_bytes": 65536
}
```

### 匹配策略

匹配器位于：

```text
backend/internal/service/openai_response_content_blocker.go
```

主要行为：

- 默认关闭，只有开启且关键词非空时才扫描。
- 关键词保存时会 trim、去空、大小写折叠去重。
- 运行时匹配为大小写不敏感匹配。
- JSON 响应会优先提取常见文本字段：
  - `delta`
  - `text`
  - `content`
  - `message`
  - `error.message`
  - `choices[].delta.content`
  - `choices[].message.content`
  - `output[].content[].text`
  - `response.output[].content[].text`
- SSE 响应会解析 `data:` payload 后扫描。
- 流式响应支持跨 chunk 关键词匹配，例如第一段输出“站点”，第二段输出“维护中”，仍可命中“站点维护中”。
- 扫描字节数受 `max_scan_bytes` 限制，避免大响应热路径开销失控。

### 拦截和 failover 行为

命中后：

1. 当前账号运行时暂停调度，原因：

   ```text
   content_blocker
   ```

2. 生成 `UpstreamFailoverError`，触发 handler 层切换其他账号。
3. 不把命中的原始上游内容写给下游客户端。
4. Ops 上游错误事件记录通用信息：

   ```text
   OpenAI upstream 200 response matched content blocker
   ```

5. failover 响应体使用通用错误：

   ```json
   {
     "error": {
       "type": "upstream_error",
       "message": "Upstream request failed",
       "code": "content_blocked"
     }
   }
   ```

为避免继续传播维护公告或广告文本，日志和下游响应都不包含命中的原始内容。

### 接入的 OpenAI 响应路径

非流式：

- `/v1/responses` 标准非流式响应
- `/v1/responses` passthrough 非流式响应
- `/v1/chat/completions` raw 非流式响应
- Responses SSE 聚合为 Chat Completions JSON 的非流式响应

流式：

- `/v1/responses` 标准 SSE
- `/v1/responses` passthrough SSE
- `/v1/chat/completions` raw SSE
- Responses SSE 转 Chat Completions SSE

### 流式场景边界

如果关键词在首个下游输出写出前命中，网关可以无痕返回 `UpstreamFailoverError`，由上层切换其他节点。

如果关键词在下游已经收到部分内容后才命中，HTTP 200 和部分 SSE 内容已经提交，网关无法再无痕切换账号。此时只能中断或返回流错误，并记录该账号冷却。为降低这种情况出现概率，建议关键词选择会在维护公告开头出现的稳定片段。

### 推荐关键词

建议配置稳定、足够长、不容易误伤正常模型输出的片段，例如：

```text
当前繁忙，休息十分钟
公益服务器压力很大
api.ranmeng.icu 提示：站点维护中
```

不建议配置过短关键词，例如：

```text
繁忙
维护
TG
```

过短关键词容易误伤正常业务文本。

### 测试覆盖

新增测试文件：

```text
backend/internal/service/openai_response_content_blocker_test.go
```

覆盖内容：

- 默认配置关闭。
- 保存配置时关键词 trim、去重。
- 开启状态下非法冷却时长和扫描字节数被拒绝。
- JSON message / Chat Completions content 命中。
- 流式分片文本跨 chunk 命中。
- 关闭状态不会命中。

## 5. 前端与后台设置变更

本分支前端修改集中在三处：

1. 分组管理页新增 OpenAI 默认 `service_tier` 下拉框。
2. 设置页“网关服务”新增自动故障转移策略卡片。
3. 设置页“网关服务”新增 200 响应内容拦截卡片。

相关文件：

- `frontend/src/views/admin/GroupsView.vue`
- `frontend/src/views/admin/SettingsView.vue`
- `frontend/src/api/admin/settings.ts`
- `frontend/src/types/index.ts`
- `frontend/src/i18n/locales/zh.ts`
- `frontend/src/i18n/locales/en.ts`

UI 风格沿用现有后台：

- 使用既有 card 容器。
- 使用 `Toggle` 作为开关。
- 使用数字输入管理阈值、窗口、冷却和扫描上限。
- 使用 textarea 管理每行一个关键词。
- 保存失败使用既有 `extractApiErrorMessage` 提示。

## 6. 运维与验证

### 本地验证命令

后端聚焦测试：

```bash
cd backend
go test ./internal/service ./internal/handler/admin ./internal/server/routes
```

前端类型检查：

```bash
cd frontend
./node_modules/.bin/vue-tsc -p tsconfig.json --noEmit
```

前端生产构建：

```bash
cd frontend
./node_modules/.bin/vue-tsc -b
./node_modules/.bin/vite build
```

后端嵌入前端构建：

```bash
cd backend
VERSION=$(tr -d '\r\n' < cmd/server/VERSION)
CGO_ENABLED=0 go build -tags embed \
  -ldflags="-s -w -X main.Version=${VERSION}" \
  -trimpath \
  -o bin/server ./cmd/server
```

健康检查：

```bash
curl -fsS http://127.0.0.1:18080/health
```

期望：

```json
{"status":"ok"}
```

### 部署注意事项

1. 需要执行数据库迁移，确保 `groups.openai_default_service_tier` 字段存在。
2. 若使用嵌入前端，需要先执行前端构建，再使用 `-tags embed` 构建后端。
3. 200 响应内容拦截默认关闭，部署后需要管理员在后台显式开启并配置关键词。
4. 对已有分组没有默认行为变化，除非管理员配置 `openai_default_service_tier`。

## 7. 回滚与降级

如需临时关闭这些能力：

### 关闭分组默认 service_tier

后台将对应 OpenAI 分组的“默认 service_tier”设为关闭，或执行：

```sql
UPDATE groups
SET openai_default_service_tier = ''
WHERE platform = 'openai';
```

### 关闭 200 响应内容拦截

后台进入：

```text
设置 -> 网关服务 -> 200 响应内容拦截
```

关闭开关即可。

### 结构化限流/冷却 failover 与自动故障转移策略

后台进入：

```text
设置 -> 网关服务 -> 自动故障转移策略
```

可关闭 `structured_400_enabled`、`http_5xx_cooldown_enabled`、`transport_cooldown_enabled`。若确需彻底恢复旧行为，需要回滚 `isOpenAIUpstreamCooldownFailoverError`、`isOpenAIUpstreamRateLimitExceededFailoverError`、连续失败计数器和 transport error failover 的相关修改。

## 8. 风险与边界

- 200 内容拦截依赖管理员配置关键词。关键词过短可能误伤正常模型输出。
- 内容拦截只扫描前 `max_scan_bytes` 字节，极晚出现的维护文本可能不会被拦截。
- 流式响应一旦已经向下游写出内容，就无法保证无痕切换账号。
- 结构化限流/冷却识别只针对 JSON 字段，不根据自然语言 message 猜测。
- 连续失败短冷却是进程内状态，多实例部署时各实例独立统计。
- 分组默认 `service_tier` 只对 OpenAI 平台分组生效。

## 9. 主要变更文件索引

数据库与模型：

- `backend/migrations/150_add_group_openai_default_service_tier.sql`
- `backend/ent/schema/group.go`
- `backend/internal/service/group.go`

OpenAI 网关：

- `backend/internal/service/openai_gateway_service.go`
- `backend/internal/service/openai_gateway_chat_completions.go`
- `backend/internal/service/openai_gateway_chat_completions_raw.go`
- `backend/internal/service/openai_gateway_messages.go`
- `backend/internal/service/openai_ws_forwarder.go`
- `backend/internal/service/openai_ws_v2_passthrough_adapter.go`
- `backend/internal/service/openai_account_runtime_block_fastpath.go`
- `backend/internal/service/openai_gateway_failover_policy.go`
- `backend/internal/service/openai_upstream_transport_error.go`
- `backend/internal/service/openai_response_content_blocker.go`

后台 API：

- `backend/internal/handler/admin/group_handler.go`
- `backend/internal/handler/admin/setting_handler.go`
- `backend/internal/handler/dto/settings.go`
- `backend/internal/server/routes/admin.go`

前端：

- `frontend/src/views/admin/GroupsView.vue`
- `frontend/src/views/admin/SettingsView.vue`
- `frontend/src/api/admin/settings.ts`
- `frontend/src/types/index.ts`
- `frontend/src/i18n/locales/zh.ts`
- `frontend/src/i18n/locales/en.ts`

测试：

- `backend/internal/service/openai_default_service_tier_test.go`
- `backend/internal/service/openai_account_runtime_block_fastpath_test.go`
- `backend/internal/service/openai_gateway_service_codex_cli_only_test.go`
- `backend/internal/service/openai_upstream_rate_limit_failover_test.go`
- `backend/internal/service/gateway_failover_policy_settings_test.go`
- `backend/internal/service/openai_response_content_blocker_test.go`
