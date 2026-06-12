# OpenAI 上游结构化错误 failover 说明

本文档说明 Sub2API 在 OpenAI 网关中如何识别部分上游返回的结构化限流、冷却错误，并自动切换到其他可用账号或节点。

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

## 识别规则

识别只依赖结构化 JSON 字段，不解析也不匹配自然语言 `message`。

### 冷却类错误

函数：

```go
isOpenAIUpstreamCooldownFailoverError(statusCode, upstreamBody)
```

命中条件：

- HTTP 状态码是 `400`
- 响应体是合法 JSON
- 满足以下任一字段：
  - `error.code == "rate_limit_cooldown"`
  - 顶层 `code == "rate_limit_cooldown"`
  - 顶层 `limit_type == "cooldown"`

### RPM 限流类错误

函数：

```go
isOpenAIUpstreamRateLimitExceededFailoverError(statusCode, upstreamBody)
```

命中条件：

- HTTP 状态码是 `400`
- 响应体是合法 JSON
- `error.code == "rate_limit_exceeded"` 或顶层 `code == "rate_limit_exceeded"`
- 顶层 `limit_type == "rpm"` 或 `error.limit_type == "rpm"`

只有 `rate_limit_exceeded` 但没有 `limit_type=rpm` 的响应不会命中该规则，避免误伤其他业务错误。

## 执行路径

命中后会进入现有 OpenAI failover 流程：

1. `shouldFailoverOpenAIUpstreamResponse` 返回 `true`。
2. 当前响应被包装为 `UpstreamFailoverError`，不把上游 message 原样返回给客户端。
3. `handleOpenAIAccountUpstreamError` 对当前 OpenAI 账号设置运行时暂停调度。
4. handler 层按已有逻辑选择其他可用账号继续重试。

账号运行时暂停时长由 `structured_400_cooldown_minutes` 控制，默认 10 分钟。未配置设置服务时回退到：

```go
openAIUpstreamCooldownFallback = 10 * time.Minute
```

运行时暂停原因：

```text
rate_limit_cooldown
rate_limit_exceeded_rpm
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

默认配置：

```json
{
  "structured_400_enabled": true,
  "structured_400_cooldown_minutes": 10,
  "failure_cooldown_jitter_percent": 20,
  "http_5xx_cooldown_enabled": true,
  "http_5xx_threshold": 3,
  "http_5xx_window_seconds": 30,
  "http_5xx_cooldown_seconds": 120,
  "transport_cooldown_enabled": true,
  "transport_threshold": 3,
  "transport_window_seconds": 30,
  "transport_cooldown_seconds": 120
}
```

字段说明：

| 字段 | 默认值 | 范围 | 说明 |
| --- | --- | --- | --- |
| `structured_400_enabled` | `true` | true/false | 是否启用结构化 `400` 限流/冷却 failover |
| `structured_400_cooldown_minutes` | `10` | 1-720 | 结构化 `400` 命中后当前账号运行时冷却时长 |
| `failure_cooldown_jitter_percent` | `20` | 0-100 | 连续失败短冷却的随机抖动比例 |
| `http_5xx_cooldown_enabled` | `true` | true/false | 是否对连续 HTTP 5xx 失败启用短冷却 |
| `http_5xx_threshold` | `3` | 1-20 | 窗口内多少次 5xx 后触发短冷却 |
| `http_5xx_window_seconds` | `30` | 1-3600 | 5xx 连续失败统计窗口 |
| `http_5xx_cooldown_seconds` | `120` | 1-7200 | 5xx 达阈值后的运行时冷却时长 |
| `transport_cooldown_enabled` | `true` | true/false | 是否对连续瞬时网络/传输失败启用短冷却 |
| `transport_threshold` | `3` | 1-20 | 窗口内多少次瞬时网络/传输失败后触发短冷却 |
| `transport_window_seconds` | `30` | 1-3600 | 瞬时网络/传输失败统计窗口 |
| `transport_cooldown_seconds` | `120` | 1-7200 | 瞬时网络/传输失败达阈值后的运行时冷却时长 |

配置保存到 DB 后会通过 `SettingService` 60 秒进程缓存被网关热路径读取，无需重启服务。

## 连续失败短冷却

除结构化 `400` 外，本分支还对 OpenAI 上游 failover 增加了两类短冷却保护：

1. HTTP `5xx`：单次 `5xx` 仍然立即触发账号 failover；同一账号在统计窗口内连续达到阈值后，运行时暂停调度一小段时间。
2. 瞬时网络/传输错误：超时、TLS、连接抖动等没有 HTTP 响应的错误会先触发 failover；同一账号连续达到阈值后，运行时短冷却。

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

## 与 200 响应内容拦截的关系

本功能处理 HTTP 错误响应，尤其是 `400` 中带结构化限流字段的情况。

管理后台的“200 响应内容拦截”处理另一类问题：上游返回 HTTP 200，但响应内容里包含维护、繁忙、换 Key 等公告文本。两者互补：

- HTTP `400 + rate_limit_cooldown/cooldown`：结构化错误 failover
- HTTP `400 + rate_limit_exceeded/rpm`：结构化错误 failover
- HTTP `200 + 维护公告文本`：200 内容关键词拦截

## 边界

- 不根据 `message` 中的“十分钟”“当前繁忙”等文本做兜底判断。
- 不从 `message` 中解析冷却时间，命中后使用 `structured_400_cooldown_minutes`，默认 10 分钟。
- 非 JSON 响应不会被结构化规则识别。
- 其他普通 `400` 参数错误仍按原有错误处理流程返回。
- 连续失败计数是进程内状态，多实例部署时各实例独立统计。
- 连续失败短冷却不替代已有 `429`、`529`、鉴权错误、模型不存在等持久状态处理。

## 验证

相关测试：

```bash
cd backend
go test ./internal/service -run 'TestGatewayFailoverPolicy|TestIsOpenAIUpstreamRateLimitExceededFailoverError|TestOpenAIUpstreamRateLimitExceededRPM'
```

相关包测试：

```bash
cd backend
go test ./internal/service ./internal/handler/admin ./internal/server/routes
```
