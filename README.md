# FlashShare

FlashShare 是面向个人 VPS 的限时会话分享服务。它适合在 Telegram、邮件或站内私信中传递兑换码、临时账号、图片和临时附件。

每条分享链接包含若干份内容。接收者打开链接后，内容按**浏览器标签页会话**分配；刷新同一标签页会继续显示原内容，新标签页会领取下一份。任意一个领取会话结束并超过关闭宽限期后，服务端立即销毁整条链接的正文、附件、领取记录和链接记录，其余未领取内容也会一并失效。

## 功能

- 管理员手动创建普通用户，无公开注册入口。
- 管理员与普通用户均可创建分享。
- 在线表格编辑，每行是一份独立内容，多列按顺序展示。
- 导入 `.xlsx` 第一个工作表，不执行公式或宏。
- 安全 Markdown、左/中/右对齐、图片和附件。
- 标签页级领取凭证，刷新不重新分配，硬截止时间不被刷新延长。
- Clash YAML、Base64 或其它文本订阅直出链接，不建立领取会话。
- 订阅成功请求计数、请求上限、无限请求、有效期与永不过期配置。
- 达到上限或过期后保留管理记录，可编辑参数并重新启用同一链接。
- WebSocket 页面租约与 15 秒关闭宽限期，服务端定时清理兜底。
- 链接有效期、当前会话时长、IPv4/IPv6 CIDR 黑白名单。
- 全局文件大小、总存储、扩展名、MIME、每用户活跃分享/订阅数、每链接内容数等限制。
- 可选的 Cloudflare Turnstile 人机验证，令牌由服务端验证后才分配内容。
- 文件使用独立派生密钥加密保存，并在读取前校验完整性。
- Docker Compose 可选 Caddy 自动 HTTPS。

## 技术栈

- Go、SQLite（WAL）
- React、TypeScript、Vite
- 单服务容器、持久化 `/data` 卷
- Caddy 反向代理

## Docker 部署

准备域名和 DNS，至少设置：

```bash
cp .env.example .env
```

生成独立主密钥并写入 `.env`：

```bash
openssl rand -hex 32
```

关键配置：

```dotenv
FLASHSHARE_IMAGE=ghcr.io/annoyingvillager/flashshare:edge
FLASHSHARE_DOMAIN=share.example.com
FLASHSHARE_PUBLIC_BASE_URL=https://share.example.com
FLASHSHARE_MASTER_KEY=这里填写64位十六进制密钥
FLASHSHARE_COOKIE_SECURE=true
FLASHSHARE_TRUST_PROXY=true
```

一键拉取 Actions 构建的镜像，并同时启动 FlashShare 与内置 Caddy：

```bash
docker compose up -d
```

Caddy 会自动申请和续期 HTTPS 证书。如果 VPS 已有 Nginx、Traefik 或其它反向代理，只启动应用：

```bash
docker compose up -d app
```

应用默认仅映射 `127.0.0.1:8080`，可由同机反向代理访问，不会直接暴露到公网。首次访问站点时创建首位管理员，之后在用户管理页手动创建普通用户。

### Cloudflare Turnstile

1. 在 Cloudflare 控制台创建 Turnstile Widget，并添加实际分享域名，本地调试时可另外添加 `localhost` 或 `127.0.0.1`。
2. 登录 FlashShare 后台，进入“设置”的 Cloudflare Turnstile 区域。
3. 填写站点密钥和私密密钥，开启开关并保存。普通设置保存在 SQLite，私密密钥使用 AES-GCM 单独加密，后台接口只返回“已配置”状态。
4. 打开一个分享链接确认挂件可以完成验证，再点击领取。

领取页使用 Cloudflare 显式渲染 API，并设置：

```text
action = flashshare_claim
cdata  = 当前分享的 publicId
```

服务端会调用 Turnstile Siteverify，同时核对 `success`、`action` 和 `cdata`。验证 token 单次有效，约 5 分钟过期；重复提交、过期 token 或其它分享生成的 token 都会被拒绝。Turnstile 服务不可达时领取接口返回 `503`，不会绕过校验直接分配内容。

Turnstile 用于降低自动化领取和脚本滥用，不能可靠识别“同一个真实用户”。同一人仍可反复完成真人验证；如果需要严格一人一份或固定冷却时间，应增加收件人身份、邀请链接或额外的领取频率策略。

更新：

```bash
docker compose pull
docker compose up -d
```

## GitHub Actions 镜像

仓库包含 `.github/workflows/docker.yml`。工作流会校验 `docker-compose.yml`；Pull Request 会构建 `linux/amd64` 镜像用于验证，推送到 `main`、创建 `v*` 标签或手动运行工作流时，会构建并推送 `linux/amd64`、`linux/arm64` 多架构镜像到 GitHub Container Registry。

`main` 分支使用 `edge` 和 `sha-*` 标签，版本标签同时生成语义化版本和 `latest` 标签：

```bash
docker pull ghcr.io/annoyingvillager/flashshare:edge
docker pull ghcr.io/annoyingvillager/flashshare:latest
```

工作流使用仓库内置的 `GITHUB_TOKEN` 和 `packages: write` 权限，不需要额外配置个人 Token。首次推送后，如果 GHCR 包仍为私有，可在 GitHub 包的 Package settings 中调整可见性。

部署时可通过 `FLASHSHARE_IMAGE` 固定版本或更换镜像仓库。默认值跟随 `main` 分支的 `edge` 标签；生产环境更建议使用 `v*` 标签生成的语义化版本，例如 `ghcr.io/annoyingvillager/flashshare:1.0.0`。

## 环境变量

| 变量 | 默认值 | 说明 |
| --- | --- | --- |
| `FLASHSHARE_DATA_DIR` | `./data` | SQLite、密钥和加密附件目录 |
| `FLASHSHARE_LISTEN_ADDR` | `:8080` | HTTP 监听地址 |
| `FLASHSHARE_PUBLIC_BASE_URL` | 空 | 生成复制链接时使用；为空则使用当前请求域名 |
| `FLASHSHARE_MASTER_KEY` | 自动生成 | 64 位十六进制主密钥；未配置时写入数据目录 `master.key` |
| `FLASHSHARE_COOKIE_SECURE` | `true` | 后台 Cookie 是否只允许 HTTPS |
| `FLASHSHARE_TRUST_PROXY` | `true` | 是否读取代理提供的客户端 IP |
| `FLASHSHARE_DOMAIN` | `share.example.com` | Compose 中 Caddy 使用的域名 |

生产环境应明确设置 `FLASHSHARE_PUBLIC_BASE_URL`。只能在其前方使用可信反向代理；应用当前读取 `X-Forwarded-For` 的最后一个地址。

## 会话和销毁语义

1. 接收者明确点击领取后，服务端在数据库事务中选择一条未领取内容。
2. 领取凭证只保存在当前标签页的 `sessionStorage`，不会写入普通 Cookie。
3. 页面刷新恢复同一领取记录，不会得到下一份内容。
4. WebSocket 连接和心跳维持页面租约。某个领取会话的连接全部断开后等待 15 秒，允许刷新时短暂重连。
5. 宽限期结束、单会话硬截止时间到达、链接有效期结束、分享者主动销毁或管理员删除用户时，立即删除整条分享。
6. 删除操作会先移除加密附件，再清零数据库中的分享和内容密文，最后删除 `shares` 记录；`items`、`claims` 和附件元数据随链接一起删除，不保留审计记录。

服务端删除完成后，分享和领取记录均不存在，原领取凭证返回 `404 Not Found`；已经建立的活动连接会收到 `4001` 关闭码。浏览器端同时清空 DOM、停止计时并撤销内存中的图片 Blob URL。

## 订阅链接

管理员和普通用户可以从控制台创建订阅。订阅内容使用单个在线编辑器录入，也可以导入 `.yaml`、`.yml` 或 `.txt` 文件。内容原样加密保存，访问时按以下规则直接输出：

- `text/yaml`：适合直接作为 Clash 配置订阅。
- `text/plain`：适合普通文本或已编码内容。
- `application/octet-stream`：按原始字节输出。
- 可选在输出前对完整内容执行标准 Base64 编码。

订阅 URL 形如 `/sub/{随机ID}/{随机Token}`，不建立标签页会话，也不要求 Turnstile。只有成功返回 `200` 的 GET 请求会增加成功请求次数；响应禁止缓存。请求上限为 `0` 表示无限，有效期配置为永不过期时使用 `0` 表示。

达到请求上限或过期后，订阅停止输出，但管理记录、加密内容和原链接 Token 保留。管理员或所属用户可重新设置请求上限与有效期，成功请求次数会清零，原链接立即恢复使用；也可永久删除订阅及内容。订阅记录计入“每用户活跃分享/订阅数”，已过期或次数用完的记录不计入活跃数量。

## 必须理解的安全边界

FlashShare 保证的是**服务端会话绑定和按时失效**，不能保证接收者设备上的内容消失。内容展示后仍可能被截图、复制、浏览器扩展读取、开发者工具保存，附件下载后也会成为普通本地文件。

网页无法把标签页刷新与关闭完全区分开，因此关闭后的销毁存在约 15 秒宽限期。浏览器崩溃、断网或系统休眠时收不到关闭事件，由服务端租约超时兜底。

公开链接不识别真实用户身份。同一个人可以用新标签页或另一浏览器领取下一份内容；IP 限制只是网络访问控制。需要严格一人一份时，应提供每收件人独立链接或增加认证，这不在第一版范围内。

订阅 Token 位于 URL 路径中，这是 Clash 等客户端直接使用订阅地址所需的兼容方式。应用访问日志会将 `/sub/` 路径统一脱敏，但外部 Nginx、Traefik 或其它反向代理仍应关闭完整 URL 访问日志或自行脱敏。

管理员拥有服务器和数据卷控制权，因此应用内加密不能防御已经获得主机控制权的管理员。管理员界面不展示其他用户的明文内容，但技术信任边界仍是服务器运营者。

Markdown 禁用原始 HTML、自动远程图片和不安全链接行为。图片、PDF 等附件始终使用领取令牌读取，响应禁用缓存；SVG、HTML、脚本和宏文件不会以内联方式执行。

启用 Turnstile 后，页面会从 `https://challenges.cloudflare.com` 加载验证挂件。内容安全策略只允许该固定来源，不开放任意第三方脚本。

## 备份

不要默认备份活动中的分享正文、附件、领取记录或订阅内容，否则从旧快照恢复可能让已销毁或停用的秘密重新出现。建议只备份用户、管理员设置和部署配置；如需完整灾备，必须接受“旧快照里仍包含历史秘密”的语义，并使用独立加密备份密钥和短保留期。

主密钥丢失后，既有分享和订阅无法解密；泄露则所有保存在数据卷中的分享密钥、订阅 Token、订阅正文和附件都可能被读取。轮换主密钥会使旧分享和订阅失效。

## 本地开发

依赖 Node.js 24、npm 与 Go 1.24+。

```bash
cd web
npm ci
npm run build

cd ..
go test ./...
FLASHSHARE_DATA_DIR=./data \
FLASHSHARE_COOKIE_SECURE=false \
go run .
```

前端开发时另开终端：

```bash
cd web
npm run dev
```

Vite 会把 `/api` 和 `/healthz` 代理到 `127.0.0.1:8080`。

## 数据目录

```text
/data/flashshare.db        SQLite 元数据
/data/master.key           未通过环境变量提供时生成的主密钥
/data/blobs/               加密附件
/data/tmp/                 容器内部临时文件
```

容器以非 root 用户运行，根文件系统只读，临时目录使用受限 `tmpfs`。应用提供 `/healthz` 健康检查。

