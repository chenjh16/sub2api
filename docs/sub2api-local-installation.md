# Sub2API 本地安装与服务化说明

生成时间：2026-06-08 13:03:00 CST
适用用户：`chenjh`
安装类型：用户级本地安装，安装在 `~/.local` 下，并通过 macOS `launchd` 作为后台服务长期运行。

> 本文档只记录安装结构、验证结果和后续维护方式，不记录任何密钥明文。数据库密码、JWT 密钥、管理员密码、OAuth 凭据等敏感内容请以本机对应文件和你自己的密码管理方式为准。

## 1. 项目分析摘要

Sub2API 不是单文件配置即可完全运行的二进制服务。它由以下部分组成：

- Go 后端：入口为 `backend/cmd/server`，配置加载在 `backend/internal/config`，首次安装逻辑在 `backend/internal/setup`。
- Vue/Vite 前端：构建产物会输出到 `backend/internal/web/dist`，后端使用 `-tags embed` 将前端打进二进制。
- PostgreSQL：必需，安装流程会创建数据库、执行 `backend/migrations/*.sql`，并写入用户/系统初始数据。
- Redis：必需，用于缓存、限流、队列等运行时能力。
- 官方部署：仓库内置 Linux/systemd 安装脚本和 Docker Compose 配置；macOS 用户级 `launchd` 需要单独适配。

官方 `deploy/install.sh` 主要面向 Linux：

- 安装目录：`/opt/sub2api`
- 配置目录：`/etc/sub2api`
- 服务管理：`systemd`
- 前置条件：已运行的 PostgreSQL 15+ 与 Redis 7+

本机参考 `CLIProxyAPI-local-installation.md` 的思路，改为：

- 不写入 `/opt`、`/etc`、`/usr/local`
- 二进制放到 `~/.local/libexec/sub2api`
- wrapper 放到 `~/.local/bin/sub2api`
- 配置/数据放到 `~/.local/share/sub2api`
- 服务使用用户级 `launchd`
- 默认只监听 `127.0.0.1:18080`

## 2. 当前构建版本

本次从源码构建的版本信息：

```text
Sub2API 0.1.135
Commit: 0aad6030
BuildType: source
Target: darwin/arm64
```

构建检查：

```sh
file backend/bin/sub2api
./backend/bin/sub2api --version
```

结果确认生成的是 macOS arm64 Mach-O 可执行文件。

## 3. 构建方式

前端依赖使用 pnpm 9，与 Dockerfile/CI 的习惯保持一致：

```sh
COREPACK_ENABLE_DOWNLOAD_PROMPT=0 corepack pnpm@9.15.9 --dir frontend install --frozen-lockfile
COREPACK_ENABLE_DOWNLOAD_PROMPT=0 corepack pnpm@9.15.9 --dir frontend run build
```

前端构建产物输出到：

```text
backend/internal/web/dist
```

后端使用 Go 自动 toolchain 下载 `go1.26.4`，再进行嵌入式前端构建：

```sh
cd backend
go mod download
VERSION_VALUE=$(tr -d '\r\n' < ./cmd/server/VERSION)
DATE_VALUE=$(date -u +%Y-%m-%dT%H:%M:%SZ)
CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build \
  -tags embed \
  -ldflags="-s -w -X main.Version=${VERSION_VALUE} -X main.Commit=0aad6030 -X main.Date=${DATE_VALUE} -X main.BuildType=source" \
  -trimpath \
  -o bin/sub2api \
  ./cmd/server
```

## 4. 安装目录结构

### 4.1 命令入口

主命令入口：

```text
/Users/chenjh/.local/bin/sub2api
```

说明：

- `sub2api` 是 wrapper 脚本，不是真实二进制。
- wrapper 会设置 `DATA_DIR`、`SERVER_HOST`、`SERVER_PORT`、`GIN_MODE` 等运行环境。
- `~/.local/bin` 已在当前用户常用命令路径中，因此可以直接运行 `sub2api`。

### 4.2 真实二进制

真实程序位于：

```text
/Users/chenjh/.local/libexec/sub2api/sub2api
```

### 4.3 配置与数据目录

主配置/数据目录：

```text
/Users/chenjh/.local/share/sub2api
```

当前文件：

```text
/Users/chenjh/.local/share/sub2api/config.example.yaml
/Users/chenjh/.local/share/sub2api/config.yaml
/Users/chenjh/.local/share/sub2api/.installed
/Users/chenjh/.local/share/sub2api/local-credentials.txt
```

说明：

- `config.yaml` 是当前服务实际使用的主配置，权限为 `0600`。
- `.installed` 是安装锁文件，用于防止重新进入首次设置流程。
- `local-credentials.txt` 保存本机初始化生成的管理员密码、数据库密码、JWT secret 和 TOTP encryption key，权限为 `0600`。
- 本文档不记录这些密钥明文。

### 4.4 文档目录

随源码复制的 README、LICENSE、开发指南位于：

```text
/Users/chenjh/.local/share/doc/sub2api
```

## 5. Wrapper 行为说明

wrapper 文件：

```text
/Users/chenjh/.local/bin/sub2api
```

默认环境：

```text
DATA_DIR=/Users/chenjh/.local/share/sub2api
SERVER_HOST=127.0.0.1
SERVER_PORT=18080
GIN_MODE=release
```

支持的管理命令：

```sh
sub2api start
sub2api stop
sub2api restart
sub2api status
sub2api logs
sub2api logs -f
```

传入其它参数时，wrapper 会切换到 `DATA_DIR` 并转发给真实二进制，例如：

```sh
sub2api --version
sub2api --setup
```

不带参数运行：

```sh
sub2api
```

等价于以前台模式启动真实程序，适合临时调试。

## 6. 后台服务配置

Sub2API 当前以 macOS 用户级 `launchd` 服务运行。

LaunchAgent 文件：

```text
/Users/chenjh/Library/LaunchAgents/local.sub2api.plist
```

服务 Label：

```text
local.sub2api
```

关键配置：

```text
ProgramArguments: /Users/chenjh/.local/bin/sub2api
WorkingDirectory: /Users/chenjh/.local/share/sub2api
RunAtLoad: true
KeepAlive: true
ProcessType: Background
ThrottleInterval: 10
```

日志输出：

```text
stdout: /Users/chenjh/.local/var/log/sub2api.log
stderr: /Users/chenjh/.local/var/log/sub2api.err.log
```

## 7. 当前运行状态

当前服务状态：

```text
local.sub2api: running
Listen: 127.0.0.1:18080
Mode: normal
```

访问地址：

```text
http://127.0.0.1:18080
```

setup 状态接口：

```sh
curl http://127.0.0.1:18080/setup/status
```

当前返回：

```json
{"code":0,"data":{"needs_setup":false,"step":"completed"}}
```

健康检查：

```sh
curl http://127.0.0.1:18080/health
```

当前返回：

```json
{"status":"ok"}
```

管理员登录接口已验证：

```text
POST /api/v1/auth/login
email: admin@sub2api.local
role: admin
token_type: Bearer
```

## 8. 当前数据库与缓存

当前使用 Homebrew 启动的本机服务：

```text
postgresql@18 started
redis started
```

监听状态：

```text
PostgreSQL: 127.0.0.1:5432
Redis: 127.0.0.1:6379
```

Sub2API 数据库：

```text
database: sub2api
user: sub2api
```

初始化已完成：

```text
PostgreSQL 连接验证通过
Redis PING 验证通过
数据库迁移完成
管理员账号创建完成
config.yaml 写入完成
.installed 锁文件写入完成
正常网关模式启动完成
```

## 9. 初始化方式记录

本次采用非交互 `AUTO_SETUP=true` 初始化，使用以下参数形态：

```sh
DATA_DIR=/Users/chenjh/.local/share/sub2api \
AUTO_SETUP=true \
DATABASE_HOST=127.0.0.1 \
DATABASE_PORT=5432 \
DATABASE_USER=sub2api \
DATABASE_PASSWORD='<your-password>' \
DATABASE_DBNAME=sub2api \
DATABASE_SSLMODE=disable \
REDIS_HOST=127.0.0.1 \
REDIS_PORT=6379 \
REDIS_PASSWORD='' \
REDIS_DB=0 \
ADMIN_EMAIL=admin@sub2api.local \
ADMIN_PASSWORD='<your-admin-password>' \
JWT_SECRET='<fixed-hex-secret>' \
TOTP_ENCRYPTION_KEY='<fixed-hex-key>' \
SERVER_HOST=127.0.0.1 \
SERVER_PORT=18080 \
sub2api
```

初始化成功后生成：

```text
/Users/chenjh/.local/share/sub2api/config.yaml
/Users/chenjh/.local/share/sub2api/.installed
```

之后 `sub2api start` 进入正常网关模式。

## 10. 常用操作

查看状态：

```sh
sub2api status
```

查看日志：

```sh
sub2api logs
sub2api logs -f
```

重启服务：

```sh
sub2api restart
```

停止服务：

```sh
sub2api stop
```

前台调试：

```sh
sub2api
```

## 11. 更新方式

从源码重新构建后更新真实二进制：

```sh
install -m 0755 \
  /Users/substance/vibe/codex/CPA/sub2api/backend/bin/sub2api \
  /Users/chenjh/.local/libexec/sub2api/sub2api
```

重启服务：

```sh
sub2api restart
```

更新时不要直接覆盖：

```text
/Users/chenjh/.local/share/sub2api/config.yaml
```

应先对比新的 `config.example.yaml`，再手工合并需要的新配置项。

## 12. 卸载方式

先停止服务：

```sh
sub2api stop
```

删除 LaunchAgent：

```sh
rm -f /Users/chenjh/Library/LaunchAgents/local.sub2api.plist
```

删除命令和真实二进制：

```sh
rm -f /Users/chenjh/.local/bin/sub2api
rm -rf /Users/chenjh/.local/libexec/sub2api
```

删除配置、文档和日志：

```sh
rm -rf /Users/chenjh/.local/share/sub2api
rm -rf /Users/chenjh/.local/share/doc/sub2api
rm -f /Users/chenjh/.local/var/log/sub2api.log
rm -f /Users/chenjh/.local/var/log/sub2api.err.log
```

## 13. 验证记录

已通过：

```sh
COREPACK_ENABLE_DOWNLOAD_PROMPT=0 corepack pnpm@9.15.9 --dir frontend install --frozen-lockfile
COREPACK_ENABLE_DOWNLOAD_PROMPT=0 corepack pnpm@9.15.9 --dir frontend run build
go mod download
CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -tags embed ./cmd/server
sub2api --version
plutil -lint /Users/chenjh/Library/LaunchAgents/local.sub2api.plist
sub2api start
sub2api status
curl http://127.0.0.1:18080/setup/status
go test ./internal/config ./internal/setup
psql -d postgres -Atc "select current_user, current_database(), version();"
redis-cli -h 127.0.0.1 -p 6379 ping
curl http://127.0.0.1:18080/health
POST http://127.0.0.1:18080/api/v1/auth/login
```

当前状态：

```text
完整数据库初始化和正常网关模式启动已完成。
```
