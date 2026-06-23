# spec 分支 Rebase 与功能维护指南

本文档用于后续把 `spec` 分支持续 rebase 到上游 `main` 时参考。它不替代功能总览文档，而是聚焦三件事：

1. `spec` 分支相对主线到底新增了哪些能力。
2. 这些能力分布在哪些模块，rebase 时哪些文件最容易冲突。
3. 冲突解决后必须验证哪些语义，避免“能编译但行为变了”。

记录时间：2026-06-23

当前分支：`spec`

当前上一次已知主线基线：`upstream/main` / `origin/main` `4a5665da`，版本 `v0.1.137`

当前 `spec` 顶部提交：`f5d2d0fc feat(frontend): improve account list viewport`

注意：`origin/spec` 仍保留早期远端提交，当前本地 `spec` 已包含更完整的功能实现。后续推送 `spec` 大概率需要 `--force-with-lease`，不要把旧 `origin/spec` 反向 merge 回本地。

## 1. Rebase 前标准流程

每次同步上游前先确认工作区干净：

```bash
git status --short --branch --untracked-files=all
```

若有本地修改，先判断来源：

- 属于当前任务的文档或代码改动：先提交。
- 属于用户未提交改动：不要覆盖，不要 reset，先暂停并说明。
- 属于本地测试产物：加入 `.gitignore` 或删除，但不要误删源码。

拉取远端：

```bash
git fetch upstream --prune
git fetch origin --prune
```

同步本地主线：

```bash
git switch main
git merge --ff-only upstream/main
```

回到功能分支并 rebase：

```bash
git switch spec
git rebase main
```

如果本地 `main` 无法快进，优先检查是否有本地提交，不要使用 `git reset --hard`。确认本地 `main` 没有需要保留的提交后，再决定如何处理。

## 2. 当前 spec 提交序列

从旧到新：

```text
4493c2ff feat(openai): add group default service tier
072ed584 feat(openai): failover rate limit cooldown errors
1f9e8011 feat(gateway): add 200 response content blocker
e419e527 docs: document spec branch changes
15e99162 feat: add configurable OpenAI failover policy
aa1ab592 feat: support editable OpenAI failover rules
9acf76c2 style: polish failover rule editor layout
d7dab0e9 feat: merge 200 content failover into policy rules
ca939332 feat(openai): failover request too large tier errors
3e0f2e30 feat(openai): improve pool retry and document spec changes
1cf904c2 fix(openai): use fixed cooldown for structured failover rules
f5d2d0fc feat(frontend): improve account list viewport
```

这些提交最好保持为一组语义清晰的 patch stack。除非上游已经完整吸收某个能力，否则 rebase 时不要随意 drop。

## 3. 功能能力分组

### 3.1 分组默认 OpenAI service_tier

目的：

- 给 `Codex Team` 等 OpenAI 分组配置默认 fast / priority 模式。
- 下游不传 `service_tier` 时自动注入分组默认值。
- 下游显式传入 `flex`、`default`、`auto`、`priority` 等值时必须尊重客户端，不覆盖。

涉及路径：

- `/v1/responses`
- `/v1/chat/completions`
- OpenAI 兼容 `/v1/messages`
- Realtime / WebSocket `response.create`
- Responses WebSocket v2 passthrough adapter

关键文件：

- `backend/migrations/150_add_group_openai_default_service_tier.sql`
- `backend/ent/schema/group.go`
- `backend/internal/handler/admin/group_handler.go`
- `backend/internal/handler/dto/types.go`
- `backend/internal/service/group.go`
- `backend/internal/service/openai_gateway_service.go`
- `backend/internal/service/openai_ws_forwarder.go`
- `backend/internal/service/openai_ws_v2_passthrough_adapter.go`
- `frontend/src/views/admin/GroupsView.vue`
- `docs/openai-default-service-tier.md`

Rebase 语义检查：

- `fast` 兼容值应规范化为 `priority`。
- 非 OpenAI 分组保存时应清空 `openai_default_service_tier`。
- API Key 鉴权缓存必须携带分组默认 `service_tier`，否则请求转发层拿不到分组配置。

### 3.2 OpenAI 自动故障转移策略

目的：

- 把上游临时错误、伪装成功响应、公益节点维护公告等识别为网关 failover。
- 不把上游广告/维护 message 原样返回给 Agent 客户端。
- 允许管理员通过规则编辑器自定义条件。

核心配置键：

```text
gateway_failover_policy_settings
```

默认规则：

- `openai_structured_400_cooldown`
- `openai_structured_400_rpm`
- `openai_request_too_large_tier_limit`
- `openai_get_channel_failed_overloaded`
- `openai_http_5xx_threshold`
- `openai_transport_threshold`
- `openai_200_content_text`

关键文件：

- `backend/internal/service/openai_gateway_failover_policy.go`
- `backend/internal/service/openai_response_content_blocker.go`
- `backend/internal/service/openai_gateway_service.go`
- `backend/internal/service/openai_upstream_transport_error.go`
- `backend/internal/service/setting_service.go`
- `backend/internal/handler/admin/setting_handler.go`
- `backend/internal/handler/dto/settings.go`
- `backend/internal/server/routes/admin.go`
- `frontend/src/views/admin/SettingsView.vue`
- `frontend/src/api/admin/settings.ts`
- `frontend/src/types/index.ts`
- `docs/openai-upstream-error-failover.md`

Rebase 语义检查：

- 200 内容拦截不再是独立设置卡片，应作为 `openai_200_content_text` 规则存在。
- `openai_200_content_text` 默认关闭，避免误拦截正常 200 内容。
- 普通 HTTP `5xx` 的 failover 由策略规则控制，不应重新回到无条件硬编码 failover。
- 系统级硬编码 failover 状态仍保留：`401`、`402`、`403`、`429`、`529`。
- 结构化 `400 cooldown`、`400 rpm`、`413 request_too_large` 默认冷却固定 10 分钟，`jitter_percent=0`。
- `get_channel_failed` 只应精确匹配 `new_api_error` 且 message 包含“负载已经达到上限”，冷却 1 小时。
- `request_too_large` 必须要求 `error.limit_bytes` 存在，避免普通请求参数错误误触发。
- 命中 `request_too_large` 或 `get_channel_failed_overloaded` 时要清理当前 OpenAI session 绑定，避免同 session 继续粘到能力不足账号。

### 3.3 条件组规则编辑能力

目的：

- 支持 JSON/Header/Message/Body/Transport 条件分别使用 `all` / `any` 组合。
- 支持条件组嵌套，满足管理员自定义规则的自由度。
- 默认规则也可独立开关、复制和调整。

关键文件：

- `backend/internal/service/openai_gateway_failover_policy.go`
- `backend/internal/service/settings_view.go`
- `backend/internal/service/gateway_failover_policy_settings_test.go`
- `frontend/src/views/admin/SettingsView.vue`

Rebase 语义检查：

- 旧的扁平条件不需要继续兼容，当前配置应使用新的 condition group 结构。
- UI JSON 编辑和可视化编辑必须读写同一份 `gateway_failover_policy_settings`。
- `match_mode` 当前实际语义是 `first`，规则按 `priority` 从小到大匹配。

### 3.4 账号调度：打破粘性

目的：

- 高优先级且可用的账号可以在特定 sticky 类型上优先参与调度。
- 解决某些账号长期被普通 session 粘性绕过的问题。

账号 extra 字段：

```json
{
  "break_sticky_session_hash": true,
  "break_sticky_previous_response": false
}
```

关键文件：

- `backend/internal/service/account.go`
- `backend/internal/service/openai_account_scheduler.go`
- `backend/internal/service/openai_account_runtime_block_fastpath.go`
- `backend/internal/service/openai_forward_session_context.go`
- `frontend/src/components/account/EditAccountModal.vue`

Rebase 语义检查：

- 打破粘性不等于强制使用账号，仍必须满足模型、端点、transport、状态、分组等兼容性。
- 普通 session 粘性和 `previous_response_id` 粘性是独立勾选项。
- UI 中 OpenAI API Key 账号的“打破粘性”位于“池模式”之后。
- 旧字段 `break_sticky_session` 如仍存在，只作为普通 session 粘性的兼容读取；前端保存应写新字段。

### 3.5 池模式瞬时错误同账号重试

目的：

- 对不稳定但可用的池模式账号，允许瞬时网络错误在同账号上连续重试若干次。
- 持久性错误仍应冷却或暂停账号，不做同账号重试。

关键文件：

- `backend/internal/service/openai_upstream_transport_error.go`
- `backend/internal/service/openai_gateway_service.go`
- `backend/internal/service/account.go`

Rebase 语义检查：

- 只有池模式账号启用同账号重试。
- 持久错误，例如 DNS 不存在、连接拒绝、代理认证失败，不应进入同账号重试。
- 非池模式账号仍走 failover 到其他账号。
- 同账号重试状态码默认以瞬时网关错误为主，不应默认包含真实额度耗尽类 `429`。

### 3.6 账号管理页视图优化

目的：

- 在不改变账号列表列顺序的前提下，提高可见列表高度。
- 避免宽表把整个页面撑出屏幕。
- 保持右侧“操作”列 sticky 可见，编辑账号不需要滚动整个页面。

关键文件：

- `frontend/src/stores/accountPageUi.ts`
- `frontend/src/components/layout/AppHeader.vue`
- `frontend/src/components/layout/AppLayout.vue`
- `frontend/src/components/layout/AppSidebar.vue`
- `frontend/src/components/layout/TablePageLayout.vue`
- `frontend/src/views/admin/AccountsView.vue`

Rebase 语义检查：

- 账号列表列顺序保持原设计，“操作”列仍在最右侧。
- 宽表横向滚动只能发生在 `.table-wrapper` 内部，不能让 `document.body` 横向溢出。
- 收起工具栏按钮只在账号管理页显示，并放在顶栏右侧图标区域。
- 工具栏收起时隐藏筛选区和未选中状态的批量编辑栏；已有选中账号时仍显示批量操作入口。
- 左侧栏展开宽度为 `184px`，收起宽度为 `72px`，`AppLayout` 的 `lg:ml-*` 必须同步。

## 4. 最容易冲突的文件和解决原则

### 4.1 `backend/internal/service/setting_service.go`

高风险原因：

- 上游频繁新增设置缓存。
- `spec` 新增了自动故障转移策略缓存和设置读写。

解决原则：

- 保留上游新增设置，例如内容安全、session block、quota auto-pause 等。
- 保留 `gateway_failover_policy_settings` 相关缓存和读取。
- 不恢复已废弃的 `gateway_content_blocker_settings` 作为独立设置。
- 如果上游重构设置缓存结构，把 `spec` 的 failover policy 迁移到新结构，而不是复制旧代码块。

### 4.2 `backend/internal/service/openai_gateway_service.go`

高风险原因：

- 上游经常调整 OpenAI Responses、Chat Completions、Images、Cyber/Thinking/Reasoning 行为。
- `spec` 在同一层加入 service tier、failover policy、session 清理和同账号重试。

解决原则：

- 上游新协议兼容逻辑优先保留。
- `spec` 的 failover 决策入口必须仍覆盖 HTTP 响应和 transport error。
- 不把普通 `5xx` 重新写回无条件 failover。
- 已经开始向下游写响应后，不应再尝试 failover。

### 4.3 `frontend/src/views/admin/SettingsView.vue`

高风险原因：

- 上游可能新增“网关服务”设置卡片。
- `spec` 的自动故障转移策略 UI 代码较大。

解决原则：

- 保留上游新增设置区域。
- 保留自动故障转移策略区域，且仍位于“网关服务”上下文中。
- 保持复制、删除、开关、JSON 编辑、条件组编辑功能。
- UI 尺寸要与当前后台风格一致，按钮高度不要回到过大的旧样式。

### 4.4 `frontend/src/components/account/EditAccountModal.vue`

高风险原因：

- 上游可能新增账号字段、quota、OAuth、池模式配置。
- `spec` 增加 OpenAI 打破粘性细分配置。

解决原则：

- OpenAI API Key 账号中，“打破粘性”放在“池模式”之后。
- OAuth 账号仍能配置粘性细分项。
- 不要把细分项折回单个 `break_sticky_session` 开关。

### 4.5 `frontend/src/components/layout/*` 与 `AccountsView.vue`

高风险原因：

- 上游可能调整整体布局、侧边栏、表格容器。
- `spec` 新增账号页工具栏收起和横向溢出修复。

解决原则：

- `AppLayout`、`TablePageLayout` 的 `min-w-0` / `max-w-full` / `overflow-x-hidden` 约束要保留。
- 账号列表列顺序不要被改动，尤其不要把“操作”列移动到名称旁边。
- 左侧栏宽度和主内容 `margin-left` 必须成对更新。

### 4.6 数据库迁移编号

当前 `spec` 新增迁移：

```text
150_add_group_openai_default_service_tier.sql
151_migrate_gateway_failover_condition_groups.sql
152_migrate_gateway_content_blocker_to_failover_rule.sql
153_add_openai_request_too_large_failover_rule.sql
154_fix_structured_openai_failover_jitter.sql
```

解决原则：

- 如果上游新增了相同数字前缀但不同文件名，先检查迁移 runner 是否按文件名唯一执行。当前项目通常以文件名排序和记录，数字重复不一定失败，但会降低可读性。
- 如果上游新增的迁移数字已经覆盖到 `154` 或更高，为了后续维护清晰，优先把 `spec` 尚未发布到生产的迁移重命名到新的连续编号，并同步文档。
- 如果本地或生产数据库已经执行过旧文件名迁移，不要简单重命名，应新增补偿迁移。

## 5. Rebase 后必须执行的验证

### 5.1 后端聚焦测试

```bash
cd backend
go test -tags unit ./internal/service -run 'TestOpenAIDefaultServiceTier|TestGatewayFailover|TestOpenAIResponseContentBlocker|TestOpenAIUpstreamRateLimit|TestHandleOpenAIUpstreamTransportError|TestGetPoolModeRetry|TestIsPoolModeRetryableStatus|TestOpenAIAccountScheduler'
```

如果上游本次改动触及 handler 或 repository，再扩大到：

```bash
cd backend
go test -tags unit ./internal/service ./internal/handler ./internal/server ./internal/repository
```

### 5.2 前端类型检查和构建

```bash
cd frontend
./node_modules/.bin/vue-tsc --noEmit
```

```bash
COREPACK_ENABLE_DOWNLOAD_PROMPT=0 corepack pnpm@9.15.9 --dir frontend run build
```

不要直接使用未知版本的 `pnpm`。Corepack 可能尝试切换到较新 pnpm 并改动 `node_modules`。

### 5.3 浏览器验证

重点页面：

- `/admin/settings`：自动故障转移策略能加载、编辑、保存。
- `/admin/groups`：OpenAI 默认 `service_tier` 能显示和保存。
- `/admin/accounts`：工具栏收起/展开生效，页面整体无横向溢出，右侧“操作”列可见。
- 编辑 OpenAI API Key 账号：池模式后方显示“打破粘性”配置。

账号管理页最小尺寸检查：

```text
document.scrollWidth <= document.clientWidth + 1
body.scrollWidth <= body.clientWidth + 1
tableWrapper.scrollWidth > tableWrapper.clientWidth
```

含义：

- 页面整体不横向滚。
- 宽表只在表格内部横向滚。
- “操作”列仍在右侧 sticky 区域。

### 5.4 本地嵌入式部署验证

前端构建后，后端需要用 `-tags embed` 重新打包，否则本地 `launchd` 服务看到的仍是旧前端。

```bash
cd backend
VERSION_VALUE=$(tr -d '\r\n' < ./cmd/server/VERSION)
COMMIT_VALUE=$(git -C .. rev-parse --short HEAD)
DATE_VALUE=$(date -u +%Y-%m-%dT%H:%M:%SZ)
CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build \
  -tags embed \
  -ldflags="-s -w -X main.Version=${VERSION_VALUE} -X main.Commit=${COMMIT_VALUE} -X main.Date=${DATE_VALUE} -X main.BuildType=source" \
  -trimpath -o bin/sub2api ./cmd/server
install -m 0755 /Users/substance/vibe/codex/CPA/sub2api/backend/bin/sub2api /Users/chenjh/.local/libexec/sub2api/sub2api
/Users/chenjh/.local/bin/sub2api restart
curl -fsS http://127.0.0.1:18080/health
```

## 6. Rebase 后人工审查清单

逐项确认：

- 分组默认 `service_tier` 的字段、DTO、缓存、转发路径都还存在。
- 客户端显式 `service_tier` 不被覆盖。
- 自动故障转移策略仍从 `gateway_failover_policy_settings` 读取。
- `openai_200_content_text` 默认关闭。
- 结构化冷却规则 `jitter_percent=0`。
- `request_too_large` 和 `get_channel_failed_overloaded` 仍会清理 session 绑定。
- 普通 `5xx` 没有重新变成硬编码无条件 failover。
- 池模式同账号重试只处理瞬时错误。
- 持久 transport error 不做同账号重试。
- 打破粘性账号仍受模型、端点、transport、状态、分组兼容性限制。
- Settings 页保留上游新增卡片和 spec 的自动故障转移策略卡片。
- Accounts 页列顺序不变，操作列仍最右侧。
- AppLayout 和 Sidebar 宽度同步：展开 `184px`，收起 `72px`。
- `.playwright-cli/` 等本地验证产物不进入提交。

## 7. 文档维护要求

每次 rebase 后至少更新以下内容之一：

- 本文档的“当前上一次已知主线基线”和“当前 `spec` 顶部提交”。
- `docs/sub2api-mainline-functional-changes.md` 中的当前版本、验证记录和新增上游兼容说明。
- 若新增或删除默认规则，同步更新 `docs/openai-upstream-error-failover.md`。
- 若调整 UI 入口，同步更新 `docs/spec-branch-changes.md`。

文档更新应和代码行为保持一致。不要只更新功能总览而遗漏冲突处理语义，否则后续 rebase 会再次踩同样的坑。
