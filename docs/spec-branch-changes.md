# spec 分支相对 main 的变更说明

本文档说明当前 `spec` 分支相对 `main` 主线的功能性修改。记录时点为 2026-06-18，本地功能对比范围如下；本文档提交本身仅用于说明，不计入功能差异：

- 基线：`main` / `e34ad2b1`
- 功能分支：`spec` / 当前 HEAD
- 对比范围：分组默认 `service_tier`、OpenAI 上游结构化错误 failover、自动故障转移策略、200 内容公告文本规则、OpenAI 账号“打破粘性”调度，以及对应文档与测试。

## 总览

`spec` 分支围绕 OpenAI 网关稳定性和 Codex Team 使用体验做了五类增强：

1. 为 OpenAI 分组增加默认 `service_tier`，支持在分组级别自动给请求注入 `priority`、`flex` 等服务层级。
2. 识别上游结构化限流/冷却错误，避免把此类错误原样返回给客户端，改为触发账号 failover 并临时暂停该账号调度。
3. 在管理后台“网关服务”增加自动故障转移策略，支持配置结构化 `400`、`413 request_too_large`、连续 `5xx`、连续网络错误短冷却，以及 200 成功响应里的公告文本拦截。
4. 将原本独立的 200 响应内容关键词拦截并入自动故障转移规则，默认规则为 `openai_200_content_text`，可独立开关和编辑。
5. 为 OpenAI 账号增加可细分的“打破粘性”选项，允许高优先级账号按需绕过普通 session 粘性或 `previous_response_id` 粘性。

这些修改均优先遵循已有网关调度和 failover 机制，不新增独立调度器。

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

也可能返回账号 tier 无法承接当前 body 的 `413 request_too_large`：

```json
{
  "error": {
    "code": "request_too_large",
    "limit_bytes": 5242880,
    "message": "Request body exceeds your tier limit (5MB for tier 0). Please upgrade your plan or split the context.",
    "tier": 0,
    "type": "invalid_request_error"
  }
}
```

也可能返回 New API 风格的通道获取失败，例如 `API-Anyrouter-OpenAI` 在日志中出现的模型负载上限响应：

```json
{
  "error": {
    "code": "get_channel_failed",
    "message": "当前模型 gpt-5.5 负载已经达到上限，请稍后重试",
    "type": "new_api_error"
  }
}
```

这类响应虽然可能是 HTTP 400/413/500，但实际语义是上游账号、上游公益 Key 或上游节点临时不可用或能力不足。如果原样转发给 Agent 客户端，客户端无法自动切换其他账号，体验会变成显式失败。

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

请求体超过上游套餐限制命中条件必须同时满足：

- HTTP 状态码为 `413 Request Entity Too Large`
- `error.code == "request_too_large"` 或顶层 `code == "request_too_large"`
- `error.limit_bytes` 存在

New API 通道获取失败且模型负载达到上限命中条件必须同时满足：

- HTTP 状态码在 `400-599`
- `error.code == "get_channel_failed"` 或顶层 `code == "get_channel_failed"`
- `error.type == "new_api_error"` 或顶层 `type == "new_api_error"`
- `error.message` 或顶层 `message` 包含 `负载已经达到上限`

默认策略整体优先识别结构化字段；`openai_get_channel_failed_overloaded` 是有意保留的精确 message 限定，用于避免把其他 `get_channel_failed` 场景全部冷却 1 小时。

### failover 行为

命中后：

1. `shouldFailoverOpenAIUpstreamResponse` 返回 true。
2. 当前上游错误进入既有 `UpstreamFailoverError` 路径。
3. `handleOpenAIAccountUpstreamError` 对当前 OpenAI 账号设置运行时暂停调度。
4. 暂停时长由命中规则决定，常规结构化限流为 10 分钟，`openai_get_channel_failed_overloaded` 为 1 小时。
5. 对 `openai_request_too_large_tier_limit` 和 `openai_get_channel_failed_overloaded` 规则额外清理当前 OpenAI sticky session 绑定，避免后续同 session 请求继续优先尝试同一能力不足或负载已满账号。
6. handler 层继续按已有逻辑选择其他可用账号重试。

运行时暂停原因：

```text
rate_limit_cooldown
rate_limit_exceeded_rpm
request_too_large_tier_limit
get_channel_failed_overloaded
```

### 影响范围

主要作用于 OpenAI 网关 HTTP 错误响应处理路径，包括 Responses、Chat Completions、Messages、Embeddings、Images 等复用 `shouldFailoverOpenAIUpstreamResponse` 或 `handleOpenAIAccountUpstreamError` 的转发路径。

### 兼容性

- 只改变结构化 `rate_limit_cooldown`、`rate_limit_exceeded + limit_type=rpm`、`413 request_too_large + error.limit_bytes`、`get_channel_failed + new_api_error + 负载已经达到上限` 的处理方式。
- 不会根据 message 中的“十分钟”等自然语言进行硬编码判断。
- 只有 `rate_limit_exceeded` 但没有 `limit_type=rpm` 的 HTTP 400 响应仍走原有错误处理规则。
- 没有 `error.limit_bytes` 的普通 `request_too_large` 不会命中默认 413 规则；管理员可自行新增更宽松的规则。
- 没有 `负载已经达到上限` 文案的普通 `get_channel_failed` 不会命中新默认规则；如需覆盖可在自动故障转移策略中新增规则。
- 如果上游返回其他 400 错误，仍走原有错误处理规则。

### 测试覆盖

新增和更新测试覆盖：

- 结构化 `rate_limit_cooldown` 被识别为 failover。
- 结构化 `rate_limit_exceeded + limit_type=rpm` 被识别为 failover。
- `413 request_too_large + error.limit_bytes` 被识别为 failover，并清理当前 OpenAI sticky session 绑定。
- `get_channel_failed + new_api_error + 负载已经达到上限` 被识别为 failover，运行时冷却 1 小时，并清理当前 OpenAI sticky session 绑定。
- 命中后账号按规则配置进入运行时调度暂停。
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
- `413 request_too_large + error.limit_bytes` 默认进入 failover，对当前账号运行时冷却，并清理当前 OpenAI sticky session 绑定。
- HTTP `5xx` 单次仍立即 failover；同一账号连续达到阈值后，运行时短冷却。
- 瞬时网络/传输错误默认返回 `UpstreamFailoverError`；同一账号连续达到阈值后，运行时短冷却。
- HTTP `200 OK` 中的维护、繁忙、公告文本可通过默认关闭的内容规则触发 failover。
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

当前实现已从固定字段升级为管理员可编辑规则列表，并直接使用新的条件组格式。`structured_400_*`、`http_5xx_*`、`transport_*` 等旧固定字段不再作为配置入口；当配置为空时，系统使用内置默认规则。

主配置结构：

```json
{
  "match_mode": "first",
  "rules": [
    {
      "id": "openai_structured_400_rpm",
      "name": "结构化 400 RPM 限流",
      "enabled": true,
      "priority": 110,
      "event": "http_response",
      "match": {
        "status_codes": [400],
        "json_condition_group": {
          "logic": "all",
          "conditions": [
            { "paths": ["error.code", "code"], "op": "equals", "value": "rate_limit_exceeded" },
            { "paths": ["limit_type", "error.limit_type"], "op": "equals", "value": "rpm" }
          ]
        }
      },
      "action": {
        "failover": true,
        "cooldown_scope": "runtime",
        "cooldown_seconds": 600,
        "jitter_percent": 0,
        "reason": "rate_limit_exceeded_rpm"
      }
    }
  ]
}
```

默认规则：

| 规则 ID | 事件 | 默认优先级 | 行为 |
| --- | --- | --- | --- |
| `openai_structured_400_cooldown` | `http_response` | 100 | 识别 `rate_limit_cooldown` 或 `limit_type=cooldown`，自动 failover，运行时冷却 10 分钟 |
| `openai_structured_400_rpm` | `http_response` | 110 | 识别 `rate_limit_exceeded + limit_type=rpm`，自动 failover，运行时冷却 10 分钟 |
| `openai_request_too_large_tier_limit` | `http_response` | 120 | 识别 `413 request_too_large + error.limit_bytes`，自动 failover，运行时冷却 10 分钟，并清理当前 OpenAI session 绑定 |
| `openai_get_channel_failed_overloaded` | `http_response` | 130 | 识别 `get_channel_failed + new_api_error + 负载已经达到上限`，自动 failover，运行时冷却 1 小时，并清理当前 OpenAI session 绑定 |
| `openai_http_5xx_threshold` | `http_response` | 200 | 普通 `5xx` 每次自动 failover，连续达到阈值后运行时短冷却 |
| `openai_transport_threshold` | `transport_error` | 300 | 瞬时网络错误每次自动 failover，连续达到阈值后运行时短冷却 |
| `openai_200_content_text` | `http_response` | 400 | 默认关闭；识别伪装成 `200 OK` 的维护、繁忙或公告文本，自动 failover，运行时冷却 10 分钟 |

规则支持：

- `event`: `http_response`、`transport_error`
- HTTP 匹配：`status_codes`、`status_ranges`、`exclude_status_codes`
- 结构化响应匹配：`json_condition_group`，路径使用 `gjson` 语法，支持 `path` 或 `paths`
- 响应头匹配：`header_condition_group`
- 文本匹配：`message_condition_group`、`body_condition_group`、`transport_condition_group`
- 网络错误分类：`transport_persistent`
- 内容扫描上限：`match.max_scan_bytes`，用于 200 内容规则，默认 `65536`
- 连续失败窗口：`match.consecutive.enabled/threshold/window_seconds`
- 动作：`action.failover`、`cooldown_scope`、`cooldown_seconds`、`jitter_percent`、`reason`、`clear_session_binding`
- 条件操作符：`equals`、`not_equals`、`contains`、`not_contains`、`exists`、`not_exists`、`in`、`regex`
- 每个条件组支持 `logic=all/any`、`conditions` 和递归 `groups`，可表达 `(A OR B) AND C`、`A OR (B AND C)` 等组合

管理页提供可视化规则编辑和整份策略 JSON 编辑。每条默认规则都作为普通条目展示，可以独立开关、复制、删除或改优先级。

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
rule:<rule_id>
```

触发短冷却时调用：

```go
BlockAccountScheduling(account, until, reason)
```

原因：

```text
rate_limit_cooldown
rate_limit_exceeded_rpm
request_too_large_tier_limit
http_5xx_threshold
transport_threshold
content_blocker
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

## 4. 200 内容公告文本规则

### 背景

还有一类上游不会返回 HTTP 错误，而是把维护、繁忙、换 Key、公告等内容伪装成 `200 OK` 成功响应。例如：

```text
当前繁忙，休息十分钟，tg频道：https://t.me/UniverseFederation
```

或：

```text
公益服务器压力很大，休息十分钟换key开放...
```

如果这类内容出现在 HTTP 200 或 SSE 正常输出中，原有网关会认为模型调用成功，并把它当作模型文本发给下游 Agent。HTTP 状态码级 failover 无法覆盖这种情况。

### 配置方式

该能力已经并入自动故障转移策略，不再有独立的“200 响应内容拦截”卡片、系统设置键或 Admin API。

管理后台路径仍是：

```text
管理后台 -> 设置 -> 网关服务 -> 自动故障转移策略
```

默认规则：

```json
{
  "id": "openai_200_content_text",
  "name": "200 内容公告文本",
  "enabled": false,
  "priority": 400,
  "event": "http_response",
  "match": {
    "status_codes": [200],
    "max_scan_bytes": 65536,
    "message_condition_group": {
      "logic": "any",
      "conditions": [
        { "op": "contains", "value": "当前繁忙，休息十分钟" },
        { "op": "contains", "value": "公益服务器压力很大" },
        { "op": "contains", "value": "api.ranmeng.icu 提示：站点维护中" }
      ]
    }
  },
  "action": {
    "failover": true,
    "cooldown_scope": "runtime",
    "cooldown_seconds": 600,
    "jitter_percent": 0,
    "reason": "content_blocker"
  }
}
```

管理员可以直接开启该规则，也可以复制后按上游特征自定义关键词、正则、冷却时间和优先级。若需要配置复杂逻辑，可用 `message_condition_group` 或 `body_condition_group` 的嵌套 `groups` 表达任意 AND/OR 组合。

旧配置迁移：

- 新迁移 `backend/migrations/152_migrate_gateway_content_blocker_to_failover_rule.sql` 会把旧 `gateway_content_blocker_settings` 转为 `openai_200_content_text` 规则。
- 如果旧内容拦截已启用且有关键词，迁移会保留关键词、冷却分钟数和扫描上限。
- 迁移后会删除旧 setting；运行时不再读取 `gateway_content_blocker_settings`。
- 新迁移 `backend/migrations/154_fix_structured_openai_failover_jitter.sql` 会把早期已保存默认策略中的结构化 400 / RPM / `request_too_large` 规则抖动从 `20` 修正为 `0`，确保这些规则固定冷却 10 分钟。

### 匹配策略

匹配器位于：

```text
backend/internal/service/openai_response_content_blocker.go
```

主要行为：

- 默认规则关闭，只有管理员启用对应规则后才扫描。
- 规则条件统一使用自动故障转移策略的操作符，`contains`、`regex`、`in` 等均可使用。
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
- 扫描字节数受 `match.max_scan_bytes` 限制，避免大响应热路径开销失控。

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
   OpenAI upstream 200 response matched failover policy: openai_200_content_text
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

为避免继续传播维护公告或广告文本，下游响应不包含命中的原始内容。

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

### 推荐规则条件

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

如果上游会换不同公告文案，可以把同类关键词放在同一个 `message_condition_group` 中使用 `logic=any`；如果需要限定来源，可额外配置 `header_condition_group` 或拆成更高优先级的独立规则。

### 测试覆盖

新增测试文件：

```text
backend/internal/service/openai_response_content_blocker_test.go
```

覆盖内容：

- 默认规则关闭。
- JSON message / Chat Completions content 命中。
- 流式分片文本跨 chunk 命中。
- 命中后按规则 action 触发运行态冷却。

## 5. 前端与后台设置变更

本分支前端修改集中在两处：

1. 分组管理页新增 OpenAI 默认 `service_tier` 下拉框。
2. 设置页“网关服务”新增自动故障转移策略卡片，200 内容公告文本作为其中一条默认规则编辑。

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
- 使用 JSON 条件组 textarea 管理复杂匹配条件。
- 保存失败使用既有 `extractApiErrorMessage` 提示。

## 6. OpenAI 账号“打破粘性”调度

### 背景

OpenAI 网关为了保证连续会话稳定，会优先使用 `session_hash` 粘性绑定账号。该行为适合大多数 Agent 会话，但也会带来一个副作用：某些低成本、优先级数值更小的账号即使当前可用，也可能因为已有 session 已经绑定到其他账号而长期无法参与调度。

排查 API-HHHL 一类账号时，需要同时看以下条件：

- `status=active` 且 `schedulable=true` 只是基础条件。
- `rate_limit_reset_at`、`overload_until`、`temp_unschedulable_until` 未到期时账号仍会被 `IsSchedulable()` 排除。
- `model_mapping` / `model_whitelist` 不覆盖请求模型时，账号不会进入候选。
- WS v2 请求会要求账号开启 `openai_*_responses_websockets_v2_enabled`；未开启的账号只能参与兼容的 HTTP/SSE 转发。
- 账号可用且兼容时，普通 `session_hash` 粘性仍可能先命中其他账号。

本次新增账号级配置解决最后一类问题。

### 配置方式

后台进入：

```text
账号管理 -> 编辑 OpenAI OAuth/API Key 账号 -> 打破粘性
```

该设置位于“过期时间”之前。先打开总开关，再按需勾选具体粘性类型：

- 普通 session 粘性：对应 `prompt_cache_key`、`session_hash` 等客户端会话锚点，用于让同一个 Agent 会话稳定落在同一账号上。
- `previous_response_id` 粘性：对应 OpenAI Responses / WebSocket v2 的响应链路锚点，用于让续链请求继续回到创建该 response 的账号。

保存后会在账号 `extra` 写入新的细分字段：

```json
{
  "break_sticky_session_hash": true,
  "break_sticky_previous_response": false
}
```

也可以同时开启两个细分项：

```json
{
  "break_sticky_session_hash": true,
  "break_sticky_previous_response": true
}
```

关闭总开关时删除上述字段。旧版 `break_sticky_session` 字段仍会被后端识别为“普通 session 粘性”，前端保存后会转写为新的 `break_sticky_session_hash` 字段。

### 调度语义

开启“打破粘性”的账号并不是强制无条件使用。它必须同时满足：

- 账号属于 OpenAI 平台；
- 账号当前可调度；
- 支持当前请求模型；
- 支持当前请求端点能力，例如 Chat Completions、Embeddings、Images；
- 支持当前 transport，例如 HTTP/SSE 或 Responses WebSocket v2；
- 未被本次请求排除；
- 未被运行时短冷却拦截；
- 对 compact 请求满足 compact 能力要求。

当存在至少一个满足上述条件的“打破粘性”账号时：

1. 若账号开启“普通 session 粘性”，普通 `session_hash` / `prompt_cache_key` 粘性会让路。
2. 若账号开启 `previous_response_id` 粘性，Responses / WebSocket v2 续链请求也可以优先调度到该账号，即使该 `previous_response_id` 之前绑定在其他账号上。
3. 调度器只在匹配当前粘性类型的“打破粘性”账号之间按现有规则选择。
4. 多个开启账号之间继续使用原有优先级、负载、等待队列、错误率和 TTFT 评分。
5. 选中的账号会重新绑定当前 `session_hash`，后续请求在没有更优“打破粘性”候选时仍保持正常粘性行为。

建议默认只勾选“普通 session 粘性”。只有当希望高优先级账号强制接管 Responses 续链请求时，才勾选 `previous_response_id` 粘性；如果新账号对应的上游不认识旧 response ID，后续请求可能触发已有的 `previous_response_not_found` 恢复逻辑。

高级调度器和旧版 load-awareness 回退路径都支持该能力。

### 主要实现文件

- `backend/internal/service/account.go`
- `backend/internal/service/openai_account_scheduler.go`
- `backend/internal/service/openai_gateway_service.go`
- `frontend/src/components/account/EditAccountModal.vue`
- `frontend/src/i18n/locales/zh.ts`
- `frontend/src/i18n/locales/en.ts`
- `backend/internal/service/openai_account_scheduler_test.go`

## 7. 运维与验证

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
3. 200 内容公告文本规则默认关闭，部署后需要管理员在自动故障转移策略中显式开启并配置条件。
4. 对已有分组没有默认行为变化，除非管理员配置 `openai_default_service_tier`。
5. “打破粘性”为账号级可选开关；未开启账号保持原有粘性调度行为。
6. 结构化 400 / RPM / `request_too_large` 默认规则不使用冷却抖动，默认固定冷却 10 分钟；连续 5xx 和瞬时网络错误短冷却仍可使用抖动分散恢复时间。

## 8. 回滚与降级

如需临时关闭这些能力：

### 关闭分组默认 service_tier

后台将对应 OpenAI 分组的“默认 service_tier”设为关闭，或执行：

```sql
UPDATE groups
SET openai_default_service_tier = ''
WHERE platform = 'openai';
```

### 关闭 200 内容公告文本规则

后台进入：

```text
设置 -> 网关服务 -> 自动故障转移策略
```

关闭或删除 `openai_200_content_text` 规则即可。

### 结构化限流/冷却 failover 与自动故障转移策略

后台进入：

```text
设置 -> 网关服务 -> 自动故障转移策略
```

可在“自动故障转移策略”中关闭或删除对应默认规则。若确需彻底恢复旧行为，需要回滚结构化错误 failover、连续失败计数器和 transport error failover 的相关修改。

### 关闭账号“打破粘性”

后台进入对应 OpenAI 账号编辑页，关闭“打破粘性”即可；或执行：

```sql
UPDATE accounts
SET extra = extra - 'break_sticky_session_hash'
          - 'break_sticky_previous_response'
          - 'break_sticky_session'
WHERE platform = 'openai';
```

## 9. 风险与边界

- 200 内容规则依赖管理员配置关键词或正则。关键词过短可能误伤正常模型输出。
- 200 内容规则只扫描前 `match.max_scan_bytes` 字节，极晚出现的维护文本可能不会被拦截。
- 流式响应一旦已经向下游写出内容，就无法保证无痕切换账号。
- 结构化限流/冷却识别只针对 JSON 字段，不根据自然语言 message 猜测。
- 连续失败短冷却是进程内状态，多实例部署时各实例独立统计。
- 分组默认 `service_tier` 只对 OpenAI 平台分组生效。
- “打破粘性”不会绕过账号冷却、模型限制或 transport 能力；`previous_response_id` 是否可被绕过由账号上的独立细分项控制。
- 开启 `previous_response_id` 粘性后，续链请求可能被迁移到未创建原 response 的账号，需要依赖既有 `previous_response_not_found` 恢复逻辑兜底。

## 10. 主要变更文件索引

数据库与模型：

- `backend/migrations/150_add_group_openai_default_service_tier.sql`
- `backend/migrations/151_migrate_gateway_failover_condition_groups.sql`
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
- `backend/internal/service/openai_account_scheduler.go`
- `backend/internal/service/account.go`

后台 API：

- `backend/internal/handler/admin/group_handler.go`
- `backend/internal/handler/admin/setting_handler.go`
- `backend/internal/handler/dto/settings.go`
- `backend/internal/server/routes/admin.go`

前端：

- `frontend/src/views/admin/GroupsView.vue`
- `frontend/src/views/admin/SettingsView.vue`
- `frontend/src/components/account/EditAccountModal.vue`
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
- `backend/internal/service/openai_account_scheduler_test.go`
