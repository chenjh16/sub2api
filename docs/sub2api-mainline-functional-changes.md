# Sub2API 当前分支相对主线的功能调整总览

本文档汇总当前工作区对 Sub2API 主线分支的所有功能性调整、优化和更新，便于后续代码审阅、部署验证、回滚排障和运维配置。

记录时间：2026-06-18

对比基线：

- 主线分支：`main`
- 主线提交：`e34ad2b1`
- 当前功能分支：`spec`
- 当前提交：`2530c6c0`
- 额外范围：包含当前工作区尚未提交的功能改动，尤其是 OpenAI 池模式网络错误同账号重试、账号编辑页“打破粘性”位置调整。

## 1. 整体目标

本轮改动主要围绕 OpenAI 网关在 Codex / Agent 场景下的稳定性、可控性和调度可解释性展开：

1. 支持分组级默认 OpenAI `service_tier`，让 Codex Team 等分组可以默认使用 fast / priority 模式。
2. 把更多上游临时故障、公益节点公告、限流冷却、能力不足等响应识别为网关级 failover，而不是原样返回给下游 Agent。
3. 提供管理员可编辑的自动故障转移策略，默认规则也可独立开关、复制、修改。
4. 将原独立的 200 响应内容拦截并入统一的自动故障转移规则体系。
5. 增强 OpenAI 账号调度能力，支持账号级“打破粘性”，也支持池模式下对瞬时网络错误进行同账号重试。
6. 补齐本地安装、部署和运维验证文档。

## 2. OpenAI 分组默认 service_tier

### 背景

OpenAI 的 API 请求可以通过 `service_tier` 控制服务层级。对于 Codex Team 这类分组，希望默认开启 fast 模式，但不希望要求所有客户端都显式传入该字段。

本分支新增分组级默认 `service_tier` 配置。

### 数据模型

新增迁移：

```text
backend/migrations/150_add_group_openai_default_service_tier.sql
```

新增字段：

```sql
groups.openai_default_service_tier varchar(20) NOT NULL DEFAULT ''
```

允许值：

```text
空字符串、priority、flex、auto、default、scale
```

其中 `fast` 会按既有兼容逻辑规范化为 `priority`。

### 转发行为

当请求满足以下条件时，网关会在转发前自动注入 `service_tier`：

- 当前 API Key 所属分组为 OpenAI 分组；
- 分组配置了 `openai_default_service_tier`；
- 下游请求体没有显式传入 `service_tier`。

当客户端已经传入 `service_tier` 时，网关尊重客户端值，不强行覆盖。

生效路径包括：

- OpenAI `/v1/responses`
- OpenAI `/v1/chat/completions`
- OpenAI 兼容 `/v1/messages`
- Realtime / WebSocket `response.create`
- Responses WebSocket v2 passthrough adapter

### 管理后台

分组管理页增加 OpenAI 默认 `service_tier` 下拉框：

```text
管理后台 -> 分组管理 -> 创建/编辑 OpenAI 分组 -> 默认 service_tier
```

可选项：

- 关闭
- `priority (fast)`
- `flex`
- `auto`
- `default`
- `scale`

### Codex Team 推荐配置

若希望 `Codex Team` 默认开启 OpenAI fast 模式：

```sql
UPDATE groups
SET openai_default_service_tier = 'priority'
WHERE name = 'Codex Team'
  AND platform = 'openai';
```

### 主要文件

- `backend/migrations/150_add_group_openai_default_service_tier.sql`
- `backend/ent/schema/group.go`
- `backend/internal/service/group.go`
- `backend/internal/handler/admin/group_handler.go`
- `backend/internal/handler/dto/types.go`
- `backend/internal/service/openai_gateway_service.go`
- `backend/internal/service/openai_ws_forwarder.go`
- `frontend/src/views/admin/GroupsView.vue`
- `docs/openai-default-service-tier.md`

## 3. 自动故障转移策略

### 背景

Sub2API 原本已经具备基础 failover 能力：当 OpenAI service 层返回 `UpstreamFailoverError` 且下游尚未收到响应内容时，handler 层会把当前账号排除，并尝试切换到其他可用账号。

本分支在这个机制上增加了管理员可编辑的 OpenAI 自动故障转移策略，用于识别更多上游临时故障。

### 配置入口

后台路径：

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

配置通过 `SettingService` 缓存读取，无需重启服务即可生效。

### 配置结构

当前策略使用规则列表，不再使用旧版固定字段：

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
            {
              "paths": ["error.code", "code"],
              "op": "equals",
              "value": "rate_limit_exceeded"
            },
            {
              "paths": ["limit_type", "error.limit_type"],
              "op": "equals",
              "value": "rpm"
            }
          ]
        }
      },
      "action": {
        "failover": true,
        "cooldown_scope": "runtime",
        "cooldown_seconds": 600,
        "jitter_percent": 20,
        "reason": "rate_limit_exceeded_rpm"
      }
    }
  ]
}
```

规则按 `priority` 从小到大匹配，目前 `match_mode` 使用 `first` 语义，即命中第一条启用规则后使用该规则的动作。

### 默认规则

当数据库中没有保存策略时，系统使用内置默认规则。

| 规则 ID | 默认状态 | 事件 | 行为 |
| --- | --- | --- | --- |
| `openai_structured_400_cooldown` | 启用 | `http_response` | 识别 `rate_limit_cooldown` 或 `limit_type=cooldown`，failover，运行时冷却 10 分钟 |
| `openai_structured_400_rpm` | 启用 | `http_response` | 识别 `rate_limit_exceeded + limit_type=rpm`，failover，运行时冷却 10 分钟 |
| `openai_request_too_large_tier_limit` | 启用 | `http_response` | 识别 `413 request_too_large + error.limit_bytes`，failover，运行时冷却 10 分钟，并清理当前 OpenAI session 绑定 |
| `openai_get_channel_failed_overloaded` | 启用 | `http_response` | 识别 `get_channel_failed + new_api_error + 负载已经达到上限`，failover，运行时冷却 1 小时，并清理当前 OpenAI session 绑定 |
| `openai_http_5xx_threshold` | 启用 | `http_response` | 普通 `5xx` 每次 failover；同账号连续达到阈值后运行时短冷却 |
| `openai_transport_threshold` | 启用 | `transport_error` | 瞬时网络错误每次 failover；同账号连续达到阈值后运行时短冷却 |
| `openai_200_content_text` | 默认关闭 | `http_response` | 识别伪装成 `200 OK` 的维护、繁忙或公告文本，failover，运行时冷却 10 分钟 |

### 匹配能力

规则支持以下匹配维度：

- HTTP 状态码：`status_codes`
- HTTP 状态码范围：`status_ranges`
- 排除状态码：`exclude_status_codes`
- JSON 响应字段：`json_condition_group`
- 响应头：`header_condition_group`
- 提取出的上游错误消息：`message_condition_group`
- 原始响应体文本：`body_condition_group`
- 网络错误文本：`transport_condition_group`
- 网络错误是否持久：`transport_persistent`
- 200 内容扫描上限：`max_scan_bytes`
- 连续失败窗口：`consecutive`

条件操作符包括：

```text
equals, not_equals, contains, not_contains, exists, not_exists, in, regex
```

每个条件组支持：

- `logic=all`
- `logic=any`
- 嵌套 `groups`

因此可以表达：

```text
(A OR B) AND C
A OR (B AND C)
```

HTTP 状态码、JSON、Header、Message、Body 等不同条件组之间仍按 AND 组合。如果需要跨类别 OR，建议拆成多条规则，并通过 `priority` 控制顺序。

### 动作能力

命中规则后支持：

- `failover=true`：将上游响应转换为 `UpstreamFailoverError`；
- `cooldown_scope=runtime`：仅在当前进程内短时间暂停该账号调度；
- `cooldown_scope=temp_unsched`：写入账号临时不可调度状态；
- `cooldown_seconds`：冷却秒数；
- `jitter_percent`：冷却抖动比例；
- `reason`：冷却原因；
- `clear_session_binding=true`：清理当前 OpenAI sticky session 绑定。

### 行为变化

主线中 HTTP `5xx` 通常由硬编码状态码触发 failover。本分支中，普通 `5xx` 的 failover 改由默认规则 `openai_http_5xx_threshold` 控制。

保留的系统级硬编码 failover 状态码为：

```text
401, 402, 403, 429, 529
```

这意味着管理员关闭 `openai_http_5xx_threshold` 后，普通 `5xx` 不会再被该默认规则自动切换，除非其他自定义规则命中。

### 主要文件

- `backend/internal/service/openai_gateway_failover_policy.go`
- `backend/internal/service/setting_service.go`
- `backend/internal/handler/admin/setting_handler.go`
- `backend/internal/handler/dto/settings.go`
- `backend/internal/server/routes/admin.go`
- `frontend/src/views/admin/SettingsView.vue`
- `frontend/src/api/admin/settings.ts`
- `docs/openai-upstream-error-failover.md`

## 4. 结构化上游错误自动 failover

### 400 冷却类错误

识别条件：

- HTTP 状态码为 `400`
- 响应体是合法 JSON
- 满足以下任一字段：
  - `error.code == "rate_limit_cooldown"`
  - 顶层 `code == "rate_limit_cooldown"`
  - 顶层 `limit_type == "cooldown"`

动作：

- 不把上游 message 原样返回给下游；
- 触发账号 failover；
- 当前账号运行时冷却 10 分钟。

### 400 RPM 限流类错误

识别条件：

- HTTP 状态码为 `400`
- 响应体是合法 JSON
- `error.code == "rate_limit_exceeded"` 或顶层 `code == "rate_limit_exceeded"`
- `limit_type == "rpm"` 或 `error.limit_type == "rpm"`

动作：

- 触发账号 failover；
- 当前账号运行时冷却 10 分钟。

此规则不会只凭自然语言 message 猜测限流，也不会把没有 `limit_type=rpm` 的所有 `rate_limit_exceeded` 都当作可切换错误。

### 413 request_too_large tier 限制

识别条件：

- HTTP 状态码为 `413`
- 响应体是合法 JSON
- `error.code == "request_too_large"` 或顶层 `code == "request_too_large"`
- `error.limit_bytes` 存在

典型响应：

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

动作：

- 触发 failover；
- 当前账号运行时冷却 10 分钟；
- 清理当前 OpenAI sticky session 绑定。

清理 session 绑定的原因是：这类错误通常说明当前账号 tier 或上游能力无法承接当前上下文，继续粘在同一账号上只会重复失败。

### get_channel_failed 模型负载上限

识别条件：

- HTTP 状态码在 `400-599`
- 响应体是合法 JSON
- `error.code == "get_channel_failed"` 或顶层 `code == "get_channel_failed"`
- `error.type == "new_api_error"` 或顶层 `type == "new_api_error"`
- `error.message` 或顶层 `message` 包含 `负载已经达到上限`

典型响应：

```json
{
  "error": {
    "code": "get_channel_failed",
    "message": "当前模型 gpt-5.5 负载已经达到上限，请稍后重试",
    "type": "new_api_error"
  }
}
```

动作：

- 触发 failover；
- 当前账号运行时冷却 1 小时；
- 清理当前 OpenAI sticky session 绑定。

该规则有意保留 message 限定，不会把所有 `get_channel_failed` 一律冷却 1 小时。

## 5. 200 响应内容公告文本规则

### 背景

部分上游会把维护、繁忙、公告、换 Key 等内容伪装成 `200 OK` 成功响应，例如：

```text
当前繁忙，休息十分钟，tg频道：https://t.me/UniverseFederation
```

或：

```text
公益服务器压力很大，休息十分钟换key开放...
```

如果网关只看 HTTP 状态码，这类内容会被当作模型正常输出转发给下游 Agent。

### 当前实现

本分支将 200 内容拦截并入自动故障转移策略，成为默认规则：

```text
openai_200_content_text
```

默认状态为关闭。管理员可在自动故障转移策略中开启并编辑。

默认匹配片段包括：

```text
当前繁忙，休息十分钟
公益服务器压力很大
api.ranmeng.icu 提示：站点维护中
```

### 扫描能力

支持扫描：

- 普通 JSON 响应；
- Chat Completions JSON；
- Responses JSON；
- SSE `data:` payload；
- 跨 chunk 的流式文本片段。

会优先提取常见文本字段：

- `message`
- `error.message`
- `content`
- `text`
- `delta`
- `choices[].delta.content`
- `choices[].message.content`
- `output[].content[].text`
- `response.output[].content[].text`

### 命中行为

命中后：

1. 当前账号运行时冷却，原因 `content_blocker`；
2. 返回 `UpstreamFailoverError`；
3. handler 层尝试切换其他账号；
4. 不把命中的公告文本返回给下游客户端；
5. Ops 上游错误事件只记录通用描述，避免传播公告文本。

### 迁移

新增迁移：

```text
backend/migrations/152_migrate_gateway_content_blocker_to_failover_rule.sql
```

作用：

- 将旧 `gateway_content_blocker_settings` 转换为 `openai_200_content_text` 规则；
- 保留旧关键词、冷却时间、扫描上限；
- 迁移后删除旧 setting；
- 运行时不再读取旧 setting。

### 流式限制

如果关键词在下游收到首个输出之前命中，网关可以无痕 failover。

如果关键词在已经写出部分 SSE 内容后才出现，HTTP 响应已经提交，网关无法再做到完全无痕切换，只能中断流或返回流错误并冷却账号。

因此建议配置尽量靠近公告开头的稳定长片段，避免使用过短关键词，例如 `维护`、`繁忙`、`TG`。

## 6. OpenAI 账号“打破粘性”

### 背景

OpenAI 调度器会优先尊重 sticky session，例如 `session_hash`、`prompt_cache_key`、`previous_response_id`。这能保持 Agent 会话上下文稳定，但也会导致高优先级、低成本账号在已有会话绑定到其他账号后无法被优先使用。

本分支新增账号级“打破粘性”配置。

### 配置入口

OpenAI API Key 账号：

```text
账号管理 -> 编辑账号 -> 池模式 -> 打破粘性
```

OpenAI OAuth 账号：

```text
账号管理 -> 编辑账号 -> 打破粘性
```

本次最新 UI 调整已将 OpenAI API Key 账号的“打破粘性”移动到“池模式”后面，更贴合实际排障和配置顺序。

### 配置项

总开关下提供两个细分项：

1. 普通 session 粘性
2. `previous_response_id` 粘性

保存到账号 `extra`：

```json
{
  "break_sticky_session_hash": true,
  "break_sticky_previous_response": false
}
```

旧字段兼容：

```json
{
  "break_sticky_session": true
}
```

旧字段仍会被后端识别为普通 session 粘性；前端再次保存时会转为 `break_sticky_session_hash`。

### 调度语义

开启“打破粘性”不等于无条件强制使用该账号。账号仍必须满足：

- OpenAI 平台；
- `status=active`；
- 当前可调度；
- 未被运行时冷却或临时禁用；
- 支持请求模型；
- 支持请求端点能力；
- 支持当前 transport；
- 未被本次请求排除；
- 如果是 compact 请求，账号必须满足 compact 能力。

当存在符合条件的“打破粘性”账号时：

- 若开启普通 session 粘性，则普通 `session_hash` / `prompt_cache_key` 绑定会让路；
- 若开启 `previous_response_id` 粘性，则 Responses 续链请求也可以优先尝试该账号；
- 多个打破粘性账号之间仍按原有优先级、负载、等待队列、错误率和 TTFT 评分调度；
- 选中后会重新绑定当前 session，使后续请求保持正常粘性。

### 风险提示

普通 session 粘性一般比较安全。

`previous_response_id` 粘性更激进：如果新账号的上游不认识旧 response ID，续链请求可能触发 `previous_response_not_found`，需要依赖现有恢复逻辑兜底。

### 主要文件

- `backend/internal/service/account.go`
- `backend/internal/service/openai_account_scheduler.go`
- `backend/internal/service/openai_gateway_service.go`
- `frontend/src/components/account/EditAccountModal.vue`
- `frontend/src/i18n/locales/zh.ts`
- `frontend/src/i18n/locales/en.ts`

## 7. 池模式与同账号错误重试

### 原有能力

Sub2API 已有“池模式”概念，适用于上游本身是账号池或公益池的 API Key 账号。

池模式下，部分上游错误不会立刻让网关切换账号，而是先在同一账号上重试若干次。

相关配置：

- `pool_mode`
- `pool_mode_retry_count`
- `pool_mode_retry_status_codes`

默认同账号重试次数：

```text
3
```

最大重试次数：

```text
10
```

默认重试状态码：

```text
401, 403, 429
```

管理员可在账号编辑页修改“同账号重试状态码”。

### 新增优化：池模式下瞬时网络错误同账号重试

本轮新增了一个小但关键的后端适配：

当 OpenAI API Key 账号开启池模式，并且 `pool_mode_retry_status_codes` 包含 `502` 时，以下非持久 transport 错误会按 `502` 参与同账号重试：

- EOF
- TLS bad record MAC
- 上游超时
- 临时 connection reset
- 其他未被判定为持久故障的网络抖动

持久性网络错误不会原地重试，仍会触发原有冷却或切换逻辑，例如：

- 代理认证失败；
- HTTP 代理 `407`；
- `connection refused`；
- `no route to host`；
- `network is unreachable`；
- DNS `no such host`。

### API-HHHL 推荐配置

对于 `API-HHHL` 这类不稳定但低成本、可用时价值较高的账号，推荐：

```text
开启池模式
同账号重试次数：2 或 3
同账号重试状态码：502, 503, 524
```

是否加入 `429` 需要谨慎：

- 如果上游 `429` 只是瞬时拥塞，可以加入；
- 如果上游 `429` 是真实额度耗尽、usage limit 或长时间 cooldown，加入会拖慢请求。

结合当前日志观察，`API-HHHL` 的部分 `429` 是 `The usage limit has been reached` 或 provider cooldown，因此默认不建议把 `429` 放入它的同账号重试状态码。

### 主要文件

- `backend/internal/service/account.go`
- `backend/internal/service/openai_upstream_transport_error.go`
- `backend/internal/service/openai_upstream_transport_error_handle_test.go`
- `frontend/src/components/account/EditAccountModal.vue`

## 8. 调度和运行时冷却优化

### 打破粘性优先调度

默认 OpenAI 调度器和旧版 load-awareness 回退路径都已支持“打破粘性”。

新增调度字段：

```go
BreakStickyOnly
BreakStickyKind
```

调度层会在普通 sticky session 命中前，先寻找当前请求兼容的打破粘性账号。

### 运行时短冷却

新增 OpenAI 进程内连续失败计数器：

```text
openaiConsecutiveFailureCounters
```

计数类别按规则隔离，例如：

```text
rule:openai_http_5xx_threshold
rule:openai_transport_threshold
```

当同一账号在窗口期内连续命中阈值后，网关调用：

```go
BlockAccountScheduling(account, until, reason)
```

该冷却只影响当前进程，不写入账号数据库状态。这样可以快速绕开抖动账号，同时避免把短暂上游波动固化成持久错误状态。

### 成功后清理失败计数

当账号成功收到非错误 HTTP 响应后，网关会清理该账号的连续失败计数，避免非连续错误被累计成冷却。

## 9. 前端管理界面更新

### 分组管理

新增：

- OpenAI 默认 `service_tier` 下拉框；
- 创建和编辑时都支持保存；
- 非 OpenAI 分组会清空该字段。

### 设置页

新增：

- 网关服务中的“自动故障转移策略”区域；
- 默认规则列表；
- 每条规则独立开关；
- 可视化编辑主要字段；
- JSON 编辑整份策略；
- 支持规则复制、删除和优先级调整。

200 内容拦截不再作为独立设置卡片存在，而是以规则 `openai_200_content_text` 出现在自动故障转移策略中。

### 账号编辑页

新增或调整：

- OpenAI 账号“打破粘性”总开关；
- 普通 session 粘性细分项；
- `previous_response_id` 粘性细分项；
- OpenAI API Key 账号中，“打破粘性”已移动到“池模式”后面；
- OAuth 账号仍在通用区域展示该配置。

## 10. 数据库迁移

本分支新增迁移：

```text
backend/migrations/150_add_group_openai_default_service_tier.sql
backend/migrations/151_migrate_gateway_failover_condition_groups.sql
backend/migrations/152_migrate_gateway_content_blocker_to_failover_rule.sql
backend/migrations/153_add_openai_request_too_large_failover_rule.sql
```

说明：

- `150`：给分组增加 OpenAI 默认 `service_tier` 字段；
- `151`：将自动故障转移策略迁移到新的 condition group 结构；
- `152`：将旧 200 内容拦截配置迁移为统一 failover 规则；
- `153`：添加 `request_too_large` tier 限制默认 failover 规则。

部署时需要确保迁移执行完成，否则后台字段或默认策略可能不完整。

## 11. 本地安装与部署文档

新增本地安装文档：

```text
docs/sub2api-local-installation.md
```

文档覆盖：

- macOS 本地安装布局；
- Homebrew PostgreSQL / Redis；
- `~/.local/libexec/sub2api/sub2api` 真实二进制；
- `~/.local/bin/sub2api` wrapper；
- `launchd` 用户级服务；
- 默认监听 `127.0.0.1:18080`；
- 前端构建；
- `-tags embed` 后端构建；
- 服务启动、停止、重启、日志查看；
- 健康检查。

当前本地部署已重新构建并重启，健康检查：

```bash
curl http://127.0.0.1:18080/health
```

返回：

```json
{"status":"ok"}
```

当前本地二进制版本：

```text
Sub2API 0.1.136
```

## 12. 验证和测试覆盖

### 已执行验证

后端聚焦测试：

```bash
cd backend
go test -tags unit ./internal/service -run 'TestHandleOpenAIUpstreamTransportError|TestGetPoolModeRetry|TestIsPoolModeRetryableStatus'
```

结果：通过。

前端类型检查：

```bash
cd frontend
./node_modules/.bin/vue-tsc --noEmit
```

结果：通过。

前端生产构建：

```bash
COREPACK_ENABLE_DOWNLOAD_PROMPT=0 corepack pnpm@9.15.9 --dir frontend run build
```

结果：通过。

本地服务健康检查：

```bash
curl http://127.0.0.1:18080/health
```

结果：通过。

### 新增和更新的测试重点

- 分组默认 `service_tier` 注入；
- 客户端显式 `service_tier` 不被覆盖；
- 结构化 `rate_limit_cooldown` failover；
- 结构化 `rate_limit_exceeded + limit_type=rpm` failover；
- `413 request_too_large + error.limit_bytes` failover 和 session 绑定清理；
- `get_channel_failed + 负载已经达到上限` failover、1 小时冷却和 session 绑定清理；
- 自动故障转移策略默认规则、条件组、Header/JSON/Message/Body/Transport 匹配；
- 200 内容文本规则；
- OpenAI 打破粘性调度；
- 池模式下瞬时 transport error 同账号重试；
- 持久性 transport error 不做同账号重试。

## 13. 运维建议

### Codex Team

建议配置：

```text
OpenAI 分组默认 service_tier = priority
```

这样下游 Codex / Agent 客户端不传 `service_tier` 时，也会默认走 fast / priority。

### API-HHHL

建议配置：

```text
开启池模式
同账号重试次数：2 或 3
同账号重试状态码：502, 503, 524
打破粘性：按需要开启普通 session 粘性
previous_response_id 粘性：谨慎开启
```

不建议默认加入 `429`，除非确认该上游的 `429` 多数是短暂拥塞，而不是真实额度耗尽。

### 200 内容公告文本

建议默认保持关闭，确认上游存在公告文本伪装成功响应后再开启。

推荐关键词应稳定且足够长，例如：

```text
当前繁忙，休息十分钟
公益服务器压力很大
api.ranmeng.icu 提示：站点维护中
```

避免使用过短关键词：

```text
繁忙
维护
TG
```

### 自动故障转移规则

建议保留默认规则作为基线。自定义规则优先使用更小的 `priority` 精确匹配特定上游，再让通用默认规则兜底。

## 14. 兼容性和边界

### 兼容性

- 分组默认 `service_tier` 默认关闭，不影响未配置分组；
- 客户端显式传入 `service_tier` 时不被覆盖；
- 200 内容文本规则默认关闭；
- 自动故障转移默认规则只处理较明确的结构化错误和网络错误；
- “打破粘性”只影响显式开启的账号；
- 池模式 transport 同账号重试只影响开启池模式且配置了 `502` 重试状态码的账号；
- 旧 `break_sticky_session` 字段仍可被后端识别。

### 边界

- 流式响应一旦已向下游写出内容，就无法保证无痕切换账号；
- 连续失败短冷却是进程内状态，多实例部署时各实例独立统计；
- 200 内容规则依赖关键词或正则质量，过短条件可能误伤正常模型输出；
- `previous_response_id` 粘性被打破后，续链请求可能被调度到不认识旧 response ID 的上游；
- `get_channel_failed` 默认规则只匹配“负载已经达到上限”，不会覆盖所有通道获取失败；
- `request_too_large` 默认规则要求 `error.limit_bytes` 存在，避免误伤普通请求参数错误。

## 15. 回滚和关闭方式

### 关闭分组默认 service_tier

后台将 OpenAI 分组的默认 `service_tier` 设为关闭，或执行：

```sql
UPDATE groups
SET openai_default_service_tier = ''
WHERE platform = 'openai';
```

### 关闭自动故障转移规则

后台进入：

```text
设置 -> 网关服务 -> 自动故障转移策略
```

关闭对应规则即可。

若只想关闭 200 内容公告文本，关闭：

```text
openai_200_content_text
```

### 关闭账号打破粘性

后台关闭账号编辑页中的“打破粘性”，或执行：

```sql
UPDATE accounts
SET extra = extra - 'break_sticky_session_hash'
          - 'break_sticky_previous_response'
          - 'break_sticky_session'
WHERE platform = 'openai';
```

### 关闭池模式同账号重试

后台关闭账号的“池模式”，或将“同账号重试次数”设为 `0`。

若只想取消网络错误同账号重试，从“同账号重试状态码”中移除：

```text
502
```

## 16. 主要文件索引

### 数据库和 Ent

- `backend/migrations/150_add_group_openai_default_service_tier.sql`
- `backend/migrations/151_migrate_gateway_failover_condition_groups.sql`
- `backend/migrations/152_migrate_gateway_content_blocker_to_failover_rule.sql`
- `backend/migrations/153_add_openai_request_too_large_failover_rule.sql`
- `backend/ent/schema/group.go`
- `backend/ent/group.go`
- `backend/ent/group_create.go`
- `backend/ent/group_update.go`

### OpenAI 网关

- `backend/internal/service/openai_gateway_service.go`
- `backend/internal/service/openai_gateway_failover_policy.go`
- `backend/internal/service/openai_response_content_blocker.go`
- `backend/internal/service/openai_forward_session_context.go`
- `backend/internal/service/openai_upstream_transport_error.go`
- `backend/internal/service/openai_account_scheduler.go`
- `backend/internal/service/account.go`
- `backend/internal/service/openai_ws_forwarder.go`
- `backend/internal/service/openai_ws_v2_passthrough_adapter.go`

### 后台 API 和设置

- `backend/internal/handler/admin/group_handler.go`
- `backend/internal/handler/admin/setting_handler.go`
- `backend/internal/handler/dto/settings.go`
- `backend/internal/handler/dto/types.go`
- `backend/internal/server/routes/admin.go`
- `backend/internal/service/setting_service.go`

### 前端

- `frontend/src/views/admin/GroupsView.vue`
- `frontend/src/views/admin/SettingsView.vue`
- `frontend/src/components/account/EditAccountModal.vue`
- `frontend/src/api/admin/settings.ts`
- `frontend/src/types/index.ts`
- `frontend/src/i18n/locales/zh.ts`
- `frontend/src/i18n/locales/en.ts`

### 文档

- `docs/openai-default-service-tier.md`
- `docs/openai-upstream-error-failover.md`
- `docs/spec-branch-changes.md`
- `docs/sub2api-local-installation.md`
- `docs/sub2api-mainline-functional-changes.md`

