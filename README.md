# Gemini-Web2API (Go Version)

将 Google Gemini Web 网页版转换为 OpenAI/Claude/Gemini 兼容的 API 格式。

## 特性

- **OpenAI 兼容**: `/v1/chat/completions`, `/v1/models`, `/v1/images/generations`, `/v1/responses`
- **Claude 兼容**: `/v1/messages`, `/v1/messages/count_tokens`
- **Gemini 原生协议**: `/v1beta/models/{model}:generateContent`, `:streamGenerateContent`
- **流式输出**: SSE (Server-Sent Events) 打字机效果
- **思考过程**: 支持提取模型思考过程 (`reasoning_content`)
- **图片生成**: 支持 Nano Banana / Nano Banana Pro 生图
- **图片上传**: 支持多模态图片输入
- **多账户负载均衡**: 支持配置多个 Google 账户
- **HTTP 代理**: 支持全局代理和每账号独立代理 (HTTP/SOCKS5)
- **IP 协议族选择**: 强制 IPv4/IPv6 出口，绕过单族风控
- **Cloudflare WARP**: 一键启用 WARP 隧道作为出口代理
- **Cookie 自动刷新**: `__Secure-1PSIDTS` 后台自动轮换，无需手动维护
- **Cookie 跨重启复用**: 过期的 PSIDTS 自动从缓存恢复，重启不中断
- **模型映射**: 支持将 Claude/OpenAI 模型名映射到 Gemini 模型
- **403 自动重试**: Cookie 过期时自动重新初始化并重试
- **结构化错误**: Gemini 上游错误（IP 封锁、用量限制等）返回清晰的错误码和操作建议

## 支持的模型

| 模型名 | 说明 |
|--------|------|
| `gemini-2.5-flash` | Flash 基础版 |
| `gemini-3.1-pro-preview` | Pro Plus 预览版 |
| `gemini-3-flash-preview` | Flash Thinking Plus 预览版 |
| `gemini-3-flash-preview-no-thinking` | Flash Plus 无思考模式 |
| `gemini-3-pro` | Pro 基础版 |
| `gemini-3-flash` | Flash 基础版 |
| `gemini-3-flash-thinking` | Flash Thinking 基础版 |
| `gemini-2.5-flash-image` | Nano Banana 生图 |
| `gemini-3-pro-image-preview` | Nano Banana Pro 生图 |

## 快速开始

### 1. 编译

```bash
# 本地编译
go build -o Gemini-Web2API.exe ./cmd/server

# 交叉编译 Linux
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o Gemini-Web2API ./cmd/server
```

### 2. 配置 Cookie

```bash
cp .env.example .env
# 编辑 .env 填入 Cookie
```

**获取 Cookie 的方式：**

| 方式 | 说明 |
|------|------|
| **浏览器手动抓取** | 无痕窗口打开 https://gemini.google.com → F12 → Application → Cookies → 复制 `__Secure-1PSID` 和 `__Secure-1PSIDTS` |
| **Firefox 自动获取** | 程序自动从 Firefox 读取（需已登录 Google） |
| **Chrome 批量获取** | `./Gemini-Web2API --fetch-cookies`（需关闭 Chrome） |

> **提示**：`__Secure-1PSIDTS` 会在数小时后过期。本程序会自动通过 Google 的 RotateCookies 接口刷新它，并缓存到 `data/cookies/` 目录，下次启动时自动复用。你只需要确保 `__Secure-1PSID` 有效即可。

**多账户配置**（带后缀）：
```env
__Secure-1PSID=xxx
__Secure-1PSIDTS=yyy
__Secure-1PSID_main=xxx
__Secure-1PSIDTS_main=yyy
ACCOUNTS=[default,main]
```

### 3. 启动

```bash
./Gemini-Web2API
```

程序会在后台加载账号，启动完成后即可接收 API 请求。

### 4. 模型映射（可选）

将外部模型名映射到 Gemini 模型：
```env
MODEL_MAPPING=claude-haiku-4-5-20251001:gemini-3-flash-preview-no-thinking
```

## API 端点

### OpenAI 兼容
```
POST /v1/chat/completions
POST /v1/images/generations
POST /v1/responses
GET  /v1/models
```

### Claude 兼容
```
POST /v1/messages
POST /v1/messages/count_tokens
GET  /v1/models/claude
```

### Gemini 原生协议
```
POST /v1beta/models/{model}:generateContent
POST /v1beta/models/{model}:streamGenerateContent
GET  /v1beta/models
```
认证支持 `Authorization: Bearer xxx`、`?key=xxx`、`x-goog-api-key` 三种方式。

## 使用示例

### 聊天
```bash
curl http://127.0.0.1:8007/v1/chat/completions \
  -H "Authorization: Bearer YOUR_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gemini-3-flash-preview",
    "messages": [{"role": "user", "content": "Hello"}],
    "stream": true
  }'
```

### 图片生成
```bash
curl http://127.0.0.1:8007/v1/images/generations \
  -H "Authorization: Bearer YOUR_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gemini-2.5-flash-image",
    "prompt": "a cat wearing a hat",
    "n": 1,
    "size": "1024x1024",
    "response_format": "b64_json"
  }'
```
或者直接在 `v1/chat/completions` 端点使用，回复将自动格式化为 `![Generated Image 1](data:image/png;base64,xxx)`。

## 网络配置

### 代理

支持 HTTP 和 SOCKS5 代理，可全局配置或按账号独立配置：

```env
# 全局代理
PROXY=socks5://127.0.0.1:7890

# 单账号覆盖
PROXY_main=http://user:pass@proxy.example.com:8080
```

### IP 协议族

当服务器的某个 IP 协议族被 Gemini 风控（常见于 IPv6 段被标记为数据中心）时，可以强制只用另一个：

```env
# 强制只用 IPv4
IP_FAMILY=ipv4

# 单账号独立设置
IP_FAMILY_main=ipv4
IP_FAMILY_backup=ipv6
```

取值（大小写不敏感）：`auto`（默认）/ `ipv4` / `ipv6`

### Cloudflare WARP

一键启用 Cloudflare WARP 作为出口代理。所有请求通过 WireGuard 隧道转发到 Cloudflare 边缘网络，获得干净的出口 IP：

```env
WARP_ENABLE=true
```

工作原理：
1. 首次启动自动向 Cloudflare 注册设备（密钥保存到 `data/warp_device.json`）
2. 创建用户态 WireGuard 隧道，暴露本地 SOCKS5 代理
3. 自动设置为默认 `PROXY`，所有账号的请求走 WARP 出口
4. 单账号可用 `PROXY_*` 覆盖

> **注意**：WARP 免费版有流量限制。如需无限流量，可绑定 Cloudflare One 付费账号。

## Cookie 自动管理

本程序内置了两层 Cookie 维护机制，确保长时间运行不会因 `__Secure-1PSIDTS` 过期而中断：

### 启动时兜底

当 `.env` 中的 `__Secure-1PSIDTS` 已过期时：
1. Init 请求拿不到 SNlM0e token
2. 自动调用 Google 的 `RotateCookies` 接口刷新 PSIDTS
3. 用新值重试 Init
4. 将新值写入 `data/cookies/<account>.json` 缓存

### 后台轮换

启动后每 10 分钟（可通过 `COOKIE_REFRESH_INTERVAL_SECONDS` 调整）自动调用 `RotateCookies`，保持 PSIDTS 始终新鲜。

### 缓存复用

重启时优先使用缓存中上次刷新的 PSIDTS（比 .env 中的旧值更新），避免启动时就触发 RotateCookies。

> **何时需要手动重抓**：当 `__Secure-1PSID` 本身过期时（登录过期、改密码、登出等），RotateCookies 会返回 401，日志会提示需要手动更新 Cookie。

## 上游错误处理

当 Gemini 返回结构化错误（而非正常响应）时，本程序会识别错误码并返回对应的 HTTP 状态码和操作建议：

| 错误码 | HTTP 状态 | 含义 | 建议 |
|--------|-----------|------|------|
| 1013 | 502 | Gemini 临时错误 | 稍后重试 |
| 1037 | 429 | 用量/速率限制 | 等几分钟或换模型 |
| 1050 | 400 | 模型与会话不一致 | 整个会话用同一模型 |
| 1052 | 400 | 模型标识过期 | 换一个模型 |
| 1060 | 429 | IP 被临时封锁/地区不支持 | 配代理或等 10-60 分钟 |

响应格式示例：
```json
{
  "error": {
    "message": "BARD_ERROR_1060: Gemini blocked the request: the server's IP is temporarily flagged...",
    "type": "gemini_api_error",
    "code": 1060
  }
}
```

## 目录结构

```
cmd/server/             # 程序入口
internal/
  adapter/              # OpenAI/Claude/Gemini 协议适配
  balancer/             # 多账户负载均衡
  browser/              # Cookie 获取
  claude/               # Claude 协议类型
  config/               # 配置（模型映射）
  gemini/               # Gemini Web API 客户端（含 RotateCookies）
  service/              # 聊天逻辑、工具调用
  storage/              # 会话持久化 + Cookie 缓存
  warp/                 # Cloudflare WARP 隧道
data/
  cookies/              # PSIDTS 缓存（自动生成）
  debug/                # 解析失败的响应 dump（自动生成）
  sessions.db           # 会话持久化数据库
  warp_device.json      # WARP 设备密钥（自动生成）
```

## 环境变量

### 核心

| 变量 | 说明 | 默认值 |
|------|------|--------|
| `PORT` | 服务端口 | 8007 |
| `PROXY_API_KEY` | API 密钥（空=无认证） | (空) |
| `LANGUAGE` | 语言（Accept-Language / payload） | en |

### Cookie

| 变量 | 说明 | 默认值 |
|------|------|--------|
| `COOKIE_REFRESH_INTERVAL_SECONDS` | PSIDTS 后台轮换间隔（秒，最小 60） | 600 |

### 网络

| 变量 | 说明 | 默认值 |
|------|------|--------|
| `PROXY` | 全局代理 (http/socks5) | (空) |
| `PROXY_{id}` | 单账号代理，覆盖全局 | (空) |
| `IP_FAMILY` | IP 协议族 (auto/ipv4/ipv6) | auto |
| `IP_FAMILY_{id}` | 单账号 IP 协议族，覆盖全局 | (空) |
| `WARP_ENABLE` | 启用 Cloudflare WARP 隧道 | false |

### 运行时

| 变量 | 说明 | 默认值 |
|------|------|--------|
| `CHAT_MODE` | 聊天模式 (normal/temporary) | normal |
| `MAX_CHARS` | 单次请求最大字符数 | 1000000 |
| `OVERSIZED_STRATEGY` | 超长消息处理策略 (compact/truncate) | compact |
| `SESSION_TTL_MINUTES` | 会话复用 TTL（分钟） | 15 |
| `WATCHDOG_TIMEOUT` | 流式响应看门狗超时（秒） | 300 |
| `SNAPSHOT_STREAMING` | 启用快照流式（实验性） | 0 |

### 存储

| 变量 | 说明 | 默认值 |
|------|------|--------|
| `STORAGE_PATH` | 会话数据库路径 | ./data/sessions.db |
| `STORAGE_MAX_SIZE_MB` | 数据库最大体积（MB） | 256 |
| `RETENTION_DAYS` | 会话保留天数 | 14 |
| `CLEANUP_INTERVAL_HOURS` | 清理间隔（小时） | 6 |

### 模型

| 变量 | 说明 | 默认值 |
|------|------|--------|
| `MODEL_MAPPING` | 模型映射 (source1:target1,source2:target2) | (空) |

## 技术栈

- **HTTP 框架**: [gin-gonic/gin](https://github.com/gin-gonic/gin)
- **TLS 指纹**: [bogdanfinn/tls-client](https://github.com/bogdanfinn/tls-client) (模拟真实浏览器)
- **Cookie 读取**: [browserutils/kooky](https://github.com/browserutils/kooky) (Firefox)
- **会话存储**: [etcd-io/bbolt](https://github.com/etcd-io/bbolt) (嵌入式 KV)
- **WireGuard**: [golang.zx2c4.com/wireguard](https://golang.zx2c4.com/wireguard) + gvisor netstack (WARP 隧道)
- **JSON 解析**: [tidwall/gjson](https://github.com/tidwall/gjson)

## 致谢

- [HanaokaYuzu/Gemini-API](https://github.com/HanaokaYuzu/Gemini-API) — Cookie 轮换机制参考
- [ViRb3/wgcf](https://github.com/ViRb3/wgcf) — WARP 注册 API 参考
- [Nativu5/Gemini-FastAPI](https://github.com/Nativu5/Gemini-FastAPI) — Python 版参考

## 注意

不适用于生产安全级。欢迎提 Issue 和 PR。
