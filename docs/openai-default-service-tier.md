# OpenAI 分组默认 service_tier

Sub2API 支持在 OpenAI 分组上配置默认 `service_tier`。当该分组的客户端请求没有显式传入 `service_tier` 时，网关会在转发到 OpenAI 前自动补上分组默认值；如果客户端已经传入 `service_tier`，Sub2API 会尊重客户端值，不会覆盖。

这个功能适合给 Codex Team 这类固定用途分组默认开启 OpenAI API 的 fast 模式。OpenAI API 当前使用 `priority` 表示 fast/priority 服务层级，因此推荐把该分组配置为 `priority`。

## 生效范围

分组默认 `service_tier` 仅作用于 OpenAI 平台分组，覆盖以下转发路径：

- `POST /v1/responses`
- `POST /v1/chat/completions`
- OpenAI 兼容的 `POST /v1/messages`
- Realtime/WebSocket 转发，包括 `response.create` 事件

当请求体已有 `service_tier` 字段时，网关只做既有规范化处理：

- `fast` 会被规范化为 `priority`
- `priority`、`flex`、`auto`、`default`、`scale` 会按客户端原值保留
- 不支持的值会被过滤，避免转发非法参数

## 后台配置

进入管理后台：

1. 打开 **管理后台 -> 分组管理**
2. 新建或编辑 OpenAI 平台分组
3. 在 **默认 service_tier** 中选择需要的值
4. 保存分组

可选值说明：

| 值 | 说明 |
| --- | --- |
| 空 | 不自动注入 `service_tier` |
| `priority` | OpenAI priority/fast 服务层级，推荐用于 Codex Team |
| `flex` | OpenAI flex 服务层级 |
| `auto` | 使用 OpenAI Project settings 中配置的服务层级 |
| `default` | 使用标准价格与性能层级 |
| `scale` | 保留给支持 Scale Tier 的请求 |

## 数据库字段

迁移文件：

```text
backend/migrations/150_add_group_openai_default_service_tier.sql
```

新增字段：

```sql
openai_default_service_tier varchar(20) NOT NULL DEFAULT ''
```

字段允许值为：

```text
''、priority、flex、auto、default、scale
```

## 给 Codex Team 开启 fast 模式

如果已经存在名为 `Codex Team` 的 OpenAI 分组，可以直接执行：

```sql
UPDATE groups
SET openai_default_service_tier = 'priority'
WHERE name = 'Codex Team'
  AND platform = 'openai';
```

配置后，Codex Team 下游客户端无需改请求体。没有显式传 `service_tier` 的 OpenAI 请求会自动以 `priority` 转发；客户端显式传入 `flex`、`default`、`auto` 等值时仍按客户端选择转发。

## 行为示例

分组配置：

```text
openai_default_service_tier = priority
```

客户端请求：

```json
{
  "model": "gpt-5.1",
  "input": "hello"
}
```

转发到 OpenAI 前：

```json
{
  "model": "gpt-5.1",
  "input": "hello",
  "service_tier": "priority"
}
```

客户端显式请求：

```json
{
  "model": "gpt-5.1",
  "input": "hello",
  "service_tier": "flex"
}
```

转发时保持为：

```json
{
  "model": "gpt-5.1",
  "input": "hello",
  "service_tier": "flex"
}
```

参考 OpenAI API 文档：[`service_tier`](https://developers.openai.com/api/reference/resources/chat/subresources/completions/methods/create) 用于指定请求处理的服务层级，未设置时默认行为为 `auto`。
