# OpenAI 上游错误自动故障转移策略说明

本文档说明 Sub2API 在 OpenAI 网关中如何通过管理员可配置规则识别上游错误，并自动切换到其他可用账号或节点。

## 背景

有些上游不会使用标准的 `429 Too Many Requests`，而是用 `400 Bad Request` 返回临时限流或维护状态。例如：

```json
{
  "error": {
    "code": "rate_limit_exceeded",
    "message": "upstream busy",
    "type": "invalid_request_error"
  },
  "code": "rate_limit_exceeded",
  "limit_type": "rpm",
  "message": "upstream busy"
}
```

这类错误对客户端来说不是参数错误，而是当前上游节点暂时不可用。若直接返回给下游 Agent，Agent 通常不会触发 Sub2API 账号切换，因此网关需要在服务端识别并进入 failover。

## 默认识别规则

默认启用规则主要依赖结构化 JSON 字段、HTTP 状态码和网络错误类型。`openai_get_channel_failed_overloaded` 是一个有意保留的精确 message 限定例外，用于识别 New API 返回的“模型负载已经达到上限”响应，避免把所有 `get_channel_failed` 场景都冷却 1 小时。另有一条默认关闭的 200 内容公告文本规则，可由管理员按需启用。

### 冷却类错误

命中条件：

- HTTP 状态码是 `400`
- 响应体是合法 JSON
- 满足以下任一字段：
  - `error.code == "rate_limit_cooldown"`
  - 顶层 `code == "rate_limit_cooldown"`
  - 顶层 `limit_type == "cooldown"`

### RPM 限流类错误

命中条件：

- HTTP 状态码是 `400`
- 响应体是合法 JSON
- `error.code == "rate_limit_exceeded"` 或顶层 `code == "rate_limit_exceeded"`
- 顶层 `limit_type == "rpm"` 或 `error.limit_type == "rpm"`

只有 `rate_limit_exceeded` 但没有 `limit_type=rpm` 的响应不会命中该规则，避免误伤其他业务错误。

### 请求体超过上游套餐限制

命中条件：

- HTTP 状态码是 `413`
- 响应体是合法 JSON
- `error.code == "request_too_large"` 或顶层 `code == "request_too_large"`
- `error.limit_bytes` 存在

这类错误常见于同一个 session 连续请求时，某个上游账号只有较低 tier，无法承接较大的上下文。默认规则会把它视为当前节点能力不足：触发 failover，运行时冷却当前账号 10 分钟，并清理当前 OpenAI sticky session 绑定，让后续同 session 请求也能重新选择其他节点。

### New API 模型负载已满

命中条件：

- HTTP 状态码在 `400-599`
- 响应体是合法 JSON
- `error.code == "get_channel_failed"` 或顶层 `code == "get_channel_failed"`
- `error.type == "new_api_error"` 或顶层 `type == "new_api_error"`
- `error.message` 或顶层 `message` 包含 `负载已经达到上限`

这类错误在当前部署日志中表现为 `API-Anyrouter-OpenAI` 对 `gpt-5.5` 连续返回 HTTP 500，message 类似：

```text
当前模型 gpt-5.5 负载已经达到上限，请稍后重试
```

默认规则 `openai_get_channel_failed_overloaded` 会立即 failover、运行时冷却当前账号 1 小时，并清理当前 OpenAI sticky session 绑定，避免同一个 session 继续粘回该账号。

## 执行路径

HTTP 错误响应进入以下流程：

1. `decideOpenAIUpstreamHTTPFailover` 先按管理员规则列表从低优先级数值到高优先级数值匹配。
2. 命中规则且 `action.failover=true` 时，当前响应被包装为 `UpstreamFailoverError`，不会把上游 message 原样返回给客户端。
3. `handleOpenAIAccountUpstreamError` 根据命中规则执行冷却副作用。
4. handler 层按已有逻辑选择其他可用账号继续重试。

未命中管理员规则时，仅以下系统级状态仍会固定触发 failover：

```text
401, 402, 403, 429, 529
```

HTTP `5xx` 不再由硬编码状态码兜底，而是由默认规则 `openai_http_5xx_threshold` 控制。管理员关闭该规则后，普通 `5xx` 不会再被自动切到其他账号，除非另有自定义规则命中。

网络/传输错误进入 `handleOpenAIUpstreamTransportError`：

- 明确持久错误仍会写入账号 `TempUnschedulableUntil`，并触发 failover。
- 瞬时错误仍触发 failover；默认规则 `openai_transport_threshold` 负责连续失败短冷却。

默认运行时暂停原因：

```text
rate_limit_cooldown
rate_limit_exceeded_rpm
request_too_large_tier_limit
get_channel_failed_overloaded
http_5xx_threshold
transport_threshold
content_blocker
```

## 自动故障转移策略配置

管理后台路径：

```text
管理后台 -> 设置 -> 网关服务 -> 自动故障转移策略
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

当前主配置格式：

```json
{
  "match_mode": "first",
  "rules": [
    {
      "id": "openai_structured_400_cooldown",
      "name": "结构化 400 冷却",
      "enabled": true,
      "priority": 100,
      "event": "http_response",
      "match": {
        "status_codes": [400],
        "json_condition_group": {
          "logic": "any",
          "conditions": [
            { "path": "error.code", "op": "equals", "value": "rate_limit_cooldown" },
            { "path": "code", "op": "equals", "value": "rate_limit_cooldown" },
            { "path": "limit_type", "op": "equals", "value": "cooldown" }
          ]
        }
      },
      "action": {
        "failover": true,
        "cooldown_scope": "runtime",
        "cooldown_seconds": 600,
        "jitter_percent": 0,
        "reason": "rate_limit_cooldown"
      }
    }
  ]
}
```

`structured_400_*`、`http_5xx_*`、`transport_*` 等旧固定字段已经移除。默认策略直接以 `rules` 的新格式保存和返回；若配置为空，Sub2API 会使用内置的 7 条默认规则。

默认规则：

| 规则 ID | 事件 | 默认优先级 | 行为 |
| --- | --- | --- | --- |
| `openai_structured_400_cooldown` | `http_response` | 100 | 识别 `rate_limit_cooldown` 或 `limit_type=cooldown`，failover，并运行时冷却 10 分钟 |
| `openai_structured_400_rpm` | `http_response` | 110 | 识别 `rate_limit_exceeded + limit_type=rpm`，failover，并运行时冷却 10 分钟 |
| `openai_request_too_large_tier_limit` | `http_response` | 120 | 识别 `413 request_too_large + error.limit_bytes`，failover，运行时冷却 10 分钟，并清理当前 OpenAI session 绑定 |
| `openai_get_channel_failed_overloaded` | `http_response` | 130 | 识别 `get_channel_failed + new_api_error + 负载已经达到上限`，failover，运行时冷却 1 小时，并清理当前 OpenAI session 绑定 |
| `openai_http_5xx_threshold` | `http_response` | 200 | `500-599` 除 `529` 外每次 failover；同账号 30 秒内连续 3 次后运行时冷却约 120 秒 |
| `openai_transport_threshold` | `transport_error` | 300 | 瞬时网络错误每次 failover；同账号 30 秒内连续 3 次后运行时冷却约 120 秒 |
| `openai_200_content_text` | `http_response` | 400 | 默认关闭；识别伪装成 `200 OK` 成功响应的维护、繁忙或公告文本，failover，并运行时冷却 10 分钟 |

规则字段说明：

| 字段 | 说明 |
| --- | --- |
| `id` | 规则唯一标识，保存时会规范化为小写字母、数字和下划线 |
| `enabled` | 单条规则独立开关 |
| `priority` | 数值越小越先匹配；当前 `match_mode` 支持 `first` |
| `event` | `http_response` 或 `transport_error` |
| `match.status_codes` | 精确 HTTP 状态码 |
| `match.status_ranges` | HTTP 状态码范围，例如 `{ "min": 500, "max": 599 }` |
| `match.exclude_status_codes` | 从命中结果中排除的状态码 |
| `match.json_condition_group` | JSON 条件组，基于 `gjson` 路径匹配响应 JSON；支持 `path` 或 `paths` |
| `match.header_condition_group` | Header 条件组，匹配响应头 |
| `match.message_condition_group` | Message 条件组，匹配提取出的上游错误 message |
| `match.body_condition_group` | Body 条件组，匹配原始响应体文本 |
| `match.transport_condition_group` | 网络错误条件组，匹配网络错误文本 |
| `match.transport_persistent` | `true` 只匹配持久网络错误，`false` 只匹配瞬时网络错误，缺省表示任意 |
| `match.max_scan_bytes` | 200 内容规则扫描响应前缀的最大字节数，默认 `65536`，范围 `1024-1048576` |
| `match.consecutive` | 连续失败窗口，命中规则每次都可以 failover，达到阈值后才执行冷却 |
| `action.failover` | 命中后是否把本次响应转换为 `UpstreamFailoverError` |
| `action.cooldown_scope` | `none`、`runtime` 或 `temp_unsched` |
| `action.cooldown_seconds` | 冷却时长，`runtime/temp_unsched` 时有效 |
| `action.jitter_percent` | 冷却随机抖动比例；结构化 400 / RPM / `request_too_large` 默认规则为 `0`，连续 5xx 和瞬时网络错误默认使用抖动 |
| `action.reason` | 写入运行时冷却日志和调度阻断的原因 |
| `action.clear_session_binding` | 命中后清理当前 OpenAI sticky session 绑定；适合账号能力不足或套餐限制类错误 |

条件操作符：

```text
equals, not_equals, contains, not_contains, exists, not_exists, in, regex
```

条件组格式：

```json
{
  "logic": "all",
  "conditions": [
    { "op": "contains", "value": "当前繁忙" }
  ],
  "groups": [
    {
      "logic": "any",
      "conditions": [
        { "op": "contains", "value": "公益服务器压力很大" },
        { "op": "contains", "value": "UniverseFederation" }
      ]
    }
  ]
}
```

每个条件组的 `logic` 可取 `all` 或 `any`，`groups` 可继续嵌套，因此可以表达 `(A OR B) AND C`、`A OR (B AND C)` 等组合。HTTP 状态码、JSON、Header、Message、Body 条件组之间仍按 AND 组合；如果需要跨类别 OR，建议拆成多条规则并用 `priority` 控制顺序。

配置保存到 DB 后会通过 `SettingService` 60 秒进程缓存被网关热路径读取，无需重启服务。

## 管理页编辑

管理页提供两种编辑方式：

- 可视化编辑：适合调整默认规则开关、优先级、状态码、连续窗口和冷却动作。
- JSON 编辑：适合批量编辑复杂 `*_condition_group` 或自定义正则规则。

每条默认规则都作为普通规则条目展示，可以独立开关、复制后修改，也可以删除。建议保留默认规则作为基线，新增自定义规则时使用更小的 `priority` 提前拦截更具体的错误。

## 连续失败短冷却

短冷却只写入进程内运行时调度阻断，不修改账号 DB 状态。这样可以快速绕开抖动账号，同时避免把短暂上游波动固化为持久配置。

明确持久的 transport 错误仍沿用原有处理：

- 代理认证失败
- HTTP 代理 `407`
- `connection refused`
- `no route to host`
- `network is unreachable`
- DNS `no such host`

这些错误会继续写入账号 `TempUnschedulableUntil`，默认临时禁用 10 分钟，并返回 `UpstreamFailoverError` 让 handler 切到其他账号。

成功收到非错误 HTTP 响应后，网关会清除该账号的连续失败计数。流式响应一旦已经向下游写出内容，仍不能无痕切换账号。

## 200 内容公告文本规则

`openai_200_content_text` 已并入自动故障转移策略，不再有单独的“200 响应内容拦截”设置项或 API。它处理另一类问题：上游返回 HTTP `200 OK`，但响应内容里包含维护、繁忙、换 Key、公告等文本。如果直接透传，下游 Agent 会把这些文本当作模型输出。

默认规则：

```json
{
  "id": "openai_200_content_text",
  "enabled": false,
  "event": "http_response",
  "priority": 400,
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

启用方式：

1. 进入 `设置 -> 网关服务 -> 自动故障转移策略`。
2. 找到 `200 内容公告文本` 规则并开启。
3. 在 `message_condition_group.conditions` 中维护关键词；复杂条件可切到 JSON 编辑模式。
4. 如需调整扫描前缀，修改 `match.max_scan_bytes`。
5. 如需调整命中后冷却，修改 `action.cooldown_seconds`。

匹配器会提取 JSON/SSE 中常见文本字段并支持跨流式 chunk 拼接，例如第一段输出“站点”，第二段输出“维护中”，仍可命中“站点维护中”。如果在首个下游输出写出前命中，网关会返回 `UpstreamFailoverError` 触发换节点，并不会把原始公告文本发给客户端；如果已经向下游写出部分流式内容，则无法再无痕切换，只能记录并中断当前流。

## 边界

- 默认启用规则不根据 `message` 中的“十分钟”“当前繁忙”等文本做兜底判断；只有管理员启用并配置 200 内容规则后，才会按规则匹配文本。
- 不从 `message` 中解析冷却时间；结构化 400、RPM、`request_too_large` 和 200 内容规则默认命中后固定冷却 10 分钟，如需调整，可直接修改对应规则的 `action.cooldown_seconds`。
- 非 JSON 响应不会被结构化 400 规则识别；200 内容规则会扫描提取文本或原始响应前缀。
- 其他普通 `400` 参数错误仍按原有错误处理流程返回。
- 连续失败计数是进程内状态，多实例部署时各实例独立统计。
- 连续失败短冷却不替代已有 `429`、`529`、鉴权错误、模型不存在等持久状态处理。

## 验证

相关测试：

```bash
cd backend
go test ./internal/service -run 'TestGatewayFailoverPolicy|TestIsOpenAIUpstreamRateLimitExceededFailoverError|TestOpenAIUpstreamRateLimitExceededRPM'
go test ./internal/service -run 'TestOpenAI200ContentRule'
```

相关包测试：

```bash
cd backend
go test ./internal/service ./internal/handler/admin ./internal/server/routes
```
