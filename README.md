# FlashShare

FlashShare 是面向个人 VPS 的限时会话分享服务。它适合在 Telegram、邮件或站内私信中传递兑换码、临时账号、图片和临时附件。

每条分享链接包含若干份内容。接收者打开链接后，内容按**浏览器标签页会话**分配；刷新同一标签页会继续显示原内容，新标签页会领取下一份。内容从首次展示开始计时，到期或最后一个页面连接关闭后，服务端销毁对应正文和附件。

## 功能

- 管理员手动创建普通用户，无公开注册入口。
- 管理员与普通用户均可创建分享。
- 在线表格编辑，每行是一份独立内容，多列按顺序展示。
- 导入 `.xlsx` 第一个工作表，不执行公式或宏。
- 安全 Markdown、左/中/右对齐、图片和附件。
- 标签页级领取凭证，刷新不重新分配，硬截止时间不被刷新延长。
- WebSocket 页面租约与 15 秒关闭宽限期，服务端定时清理兜底。
- 链接有效期、当前会话时长、IPv4/IPv6 CIDR 黑白名单。
- 全局文件大小、总存储、扩展名、MIME、每用户活跃链接数、每链接内容数等限制。
- 可选的 Cloudflare Turnstile 人机验证，令牌由服务端验证后才分配内容。
- 文件使用独立派生密钥加密保存，并在读取前校验完整性。
- Docker Compose 可选 Caddy 自动 HTTPS。

## 技术栈

- Go、SQLite（WAL）
- React、TypeScript、Vite
- 单服务容器、持久化 `/data` 卷
- Caddy 反向代理（可选 Compose profile）

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
FLASHSHARE_DOMAIN=share.example.com
FLASHSHARE_PUBLIC_BASE_URL=https://share.example.com
FLASHSHARE_MASTER_KEY=这里填写64位十六进制密钥
FLASHSHARE_COOKIE_SECURE=true
FLASHSHARE_TRUST_PROXY=true
```

使用内置 Caddy 自动申请证书并启动：

```bash
docker compose --profile caddy up -d --build
```

如果 VPS 已有 Nginx、Traefik 或其它反向代理，只启动应用：

```bash
docker compose up -d --build
```

默认仅映射 `127.0.0.1:8080`，应由同机反向代理提供 HTTPS。首次访问站点时创建首位管理员，之后在用户管理页手动创建普通用户。

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
docker compose --profile caddy up -d --build
```

## GitHub Actions 镜像

仓库包含 `.github/workflows/docker.yml`。Pull Request 会构建 `linux/amd64` 镜像用于验证；推送到 `main`、创建 `v*` 标签或手动运行工作流时，会构建并推送 `linux/amd64`、`linux/arm64` 多架构镜像到 GitHub Container Registry。

`main` 分支使用 `edge` 和 `sha-*` 标签，版本标签同时生成语义化版本和 `latest` 标签：

```bash
docker pull ghcr.io/annoyingvillager/flashshare:edge
docker pull ghcr.io/annoyingvillager/flashshare:latest
```

工作流使用仓库内置的 `GITHUB_TOKEN` 和 `packages: write` 权限，不需要额外配置个人 Token。首次推送后，如果 GHCR 包仍为私有，可在 GitHub 包的 Package settings 中调整可见性。

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
4. WebSocket 连接和心跳维持页面租约。最后一个连接断开后等待 15 秒，允许刷新时短暂重连。
5. 到达单会话硬截止时间或链接有效期后立即销毁，刷新不会续期。
6. 分享者主动销毁、管理员删除用户或服务器清理任务也会销毁对应密文和附件。

服务端删除完成后，原领取凭证返回 `410 Gone`。浏览器端同时清空 DOM、停止计时和撤销内存中的图片 Blob URL。

## 必须理解的安全边界

FlashShare 保证的是**服务端会话绑定和按时失效**，不能保证接收者设备上的内容消失。内容展示后仍可能被截图、复制、浏览器扩展读取、开发者工具保存，附件下载后也会成为普通本地文件。

网页无法把标签页刷新与关闭完全区分开，因此关闭后的销毁存在约 15 秒宽限期。浏览器崩溃、断网或系统休眠时收不到关闭事件，由服务端租约超时兜底。

公开链接不识别真实用户身份。同一个人可以用新标签页或另一浏览器领取下一份内容；IP 限制只是网络访问控制。需要严格一人一份时，应提供每收件人独立链接或增加认证，这不在第一版范围内。

管理员拥有服务器和数据卷控制权，因此应用内加密不能防御已经获得主机控制权的管理员。管理员界面不展示其他用户的明文内容，但技术信任边界仍是服务器运营者。

Markdown 禁用原始 HTML、自动远程图片和不安全链接行为。图片、PDF 等附件始终使用领取令牌读取，响应禁用缓存；SVG、HTML、脚本和宏文件不会以内联方式执行。

启用 Turnstile 后，页面会从 `https://challenges.cloudflare.com` 加载验证挂件。内容安全策略只允许该固定来源，不开放任意第三方脚本。

## 备份

不要默认备份活动中的分享正文、附件和领取记录，否则从旧快照恢复可能让已销毁秘密重新出现。建议只备份用户、管理员设置和部署配置；如需完整灾备，必须接受“旧快照里仍包含历史秘密”的语义，并使用独立加密备份密钥和短保留期。

主密钥丢失后，既有分享无法解密；泄露则所有保存在数据卷中的分享密钥和附件都可能被读取。轮换主密钥会使旧分享失效。

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

