# CloudPrint

局域网自助云打印：用户从浏览器上传文件、选择打印机、提交任务；NAS 上的
CloudPrint 服务负责排队、转换与派发。打印机地址与型号不写死在代码里，而是由
管理员添加端点，或经 mDNS / 网段扫描发现后人工审批。

## 已实现

- Go 1.27 单二进制后端，React 19 + Tailwind 4 + HeroUI 3 前端，前端资源嵌入二进制
- 账号体系：Argon2id 密码、Session + CSRF、登录限速、管理员/普通用户
- 用户组授权：管理员建组、分配成员、授权打印机；普通用户只看到并只能用所属组授权
  的打印机，权限取多组并集；创建任务时后端再次强制校验
- 打印机注册表：逻辑打印机 + 多端点 + 优先级 + 健康状态 + 能力探测
- 端点类型：`ipp`（服务端直连）、`agent`（Windows 代理，原生驱动兜底）
- 发现与审批：mDNS、受控 CIDR 扫描、手工候选；发现结果不会自动启用
- 文件流水线：PDF 校验/页数、JPG/PNG 转 PDF、可选 LibreOffice 转 Office
- 打印任务：排队、打印机级串行、状态机、SSE 实时推送、取消、每日页数额度
- 每日额度按 NAS 本地时间的零点重置
- 纯 Go IPP：Get-Printer-Attributes 探测、Validate/Print-Job、直接提交 PDF
- Windows 代理：Bearer Token 领取任务；通过显式打印命令（启动参数或管理后台配置）出纸

## 重要边界

- 型号只提供**能力档案**，不据此假定能打印 PDF。是否可 IPP 直打 PDF 一律以端点
  实际探测结果为准。
- 服务端不会把 PDF 直接写到 RAW 9100。`socket://host:9100` 只能确认端口可达，不能
  验证设备能否解析 PDF，因此不作为可调度端点；需要原生驱动时用 Windows 代理。
- 已知奔图 MS6000NW 现场情况：Windows 原生驱动 + RAW 9100 可出纸；
  WSD/IPP Class Driver 曾出现卡单（Error / Printing / Retained，PagesPrinted=0）。
  因此对这台机器优先使用 Windows 代理端点。
- 服务端不会修改打印机后台的任何设置，也不会自动发送测试页。

## 快速运行

需要 Go 1.27+ 与 Node.js 20+。

```powershell
cd web
npm install
npm run build

cd ..\backend
$env:CLOUDPRINT_ADMIN_PASSWORD = "换成一个强密码"
$env:CLOUDPRINT_DATA = "C:\CloudPrintData"
go run .\cmd\cloudprint
```

打开 `http://NAS地址:8080`。不设置 `CLOUDPRINT_ADMIN_PASSWORD` 时，首次启动会生成
一次性管理员密码并打印在控制台。

生产构建：

```powershell
cd web; npm run build
cd ..\backend
go build -trimpath -o dist\cloudprint.exe .\cmd\cloudprint
go build -trimpath -o dist\cloudprint-agent.exe .\cmd\agent
```

## NAS 部署（Docker）

```bash
cp .env.example .env
# 编辑 .env，至少设置一个强 CLOUDPRINT_ADMIN_PASSWORD
docker compose up -d --build
```

镜像包含 LibreOffice（DOCX/XLSX/PPTX 转换）与 poppler（PDF 预览）。数据持久化在
`./data`，端口默认 `8080`。反向代理时请把 `CLOUDPRINT_PUBLIC_URL` 设为外部访问地址，
并只在可信代理后开启 `CLOUDPRINT_TRUST_PROXY_HEADERS=true`。

群晖/威联通若使用 Container Manager，直接导入 `docker-compose.yml`，把 `./data`
映射到 NAS 的共享文件夹，并给容器固定局域网 IP 或保留端口。

## 接入打印机（管理员，三步）

1. **打印机 → 搜索打印机**。系统同时搜索局域网设备，通常 10–40 秒完成。
2. 在结果里核对名字，点 **添加**。已添加过的打印机不会重复出现在搜索结果里；
   找不到时可以在 **手动添加** 里填打印机地址（例如 `192.168.2.91`）。
3. 如果页面提示“需要一台电脑帮忙转发”，找一台**现在就能正常打印它**的 Windows
   电脑，用管理员身份打开 PowerShell 7，把弹窗里的整段命令粘贴运行一次。之后那台
   电脑保持开机即可。可以直接联网打印的机型不需要这一步。

最后到 **成员与权限 → 新建分组**，勾选组员和打印机。普通用户只能看到并使用本组
授权的设备。

界面不使用 IPP、mDNS、端点等技术词。真实判定只看设备能否直接接收 PDF：不能直接
接收的机型（包括奔图 MS6000NW）会显示为需要 Windows 电脑转发，避免任务提交成功
却不出纸。检测过程只读，不打印测试页，也不修改打印机设置。

## Windows 转发程序

转发程序运行在安装了打印机原生驱动、且能正常从 Windows 打印的电脑上。通常不需要
手动创建：添加打印机时如果判定需要转发，页面会直接生成安装命令。

需要重新安装、换电脑或密钥丢失时，到 **成员与权限 → 帮忙转发的电脑** 点
**重新生成安装命令**，旧密钥会立即作废。

命令行方式（调试用）：
```powershell
.\cloudprint-agent.exe `
  -server http://NAS地址:8080 `
  -token 粘贴Token `
  -endpoint ep_粘贴ID `
  -print-command 'C:\Path\SumatraPDF.exe -silent -print-to "打印机名称" "%s"'
```

`-print-command` 是实际打印动作，`%s`（或 `%FILE%`）会替换为下载后的 PDF 路径。
正常安装命令由页面生成并写入计划任务，不需要手工写。未配置打印命令时任务会失败为
`PRINT_COMMAND_MISSING`，不会伪装成功。

## 配置项

| 变量 | 默认值 | 说明 |
|---|---|---|
| `CLOUDPRINT_ADDR` | `:8080` | 监听地址 |
| `CLOUDPRINT_DATA` | `./data` | 数据目录 |
| `CLOUDPRINT_DB` | `<data>/cloudprint.db` | SQLite 路径 |
| `CLOUDPRINT_PUBLIC_URL` | 空 | 外部访问地址，用于代理下载链接 |
| `CLOUDPRINT_ADMIN_USER` | `admin` | 初始管理员用户名 |
| `CLOUDPRINT_ADMIN_PASSWORD` | 随机 | 初始管理员密码，仅首次建库生效 |
| `CLOUDPRINT_SESSION_HOURS` | `12` | 会话有效期 |
| `CLOUDPRINT_DAILY_QUOTA` | `50` | 新用户默认每日页数，0 为不限 |
| `CLOUDPRINT_MAX_UPLOAD_MB` | `50` | 单文件上限 |
| `CLOUDPRINT_RETENTION_DAYS` | `7` | 文件保留天数 |
| `CLOUDPRINT_ENABLE_MDNS` | `true` | 是否启用 mDNS 发现 |
| `CLOUDPRINT_SCAN_CIDRS` | 空 | 额外扫描网段，逗号分隔；留空则自动使用本机网卡所在网段 |
| `CLOUDPRINT_ALLOW_PRIVATE_SCAN` | `true` | 是否允许网段扫描 |
| `CLOUDPRINT_LIBREOFFICE` | 自动查找 | `soffice` 路径 |
| `CLOUDPRINT_PDFTOPPM` | 自动查找 | `pdftoppm` 路径 |
| `CLOUDPRINT_COOKIE_SECURE` | `false` | HTTPS 部署时设为 true |
| `CLOUDPRINT_TRUST_PROXY_HEADERS` | `false` | 仅可信反向代理后开启 |

打开 `CLOUDPRINT_TRUST_PROXY_HEADERS` 后才会读取 `X-Forwarded-For` /
`X-Real-IP` / `X-Forwarded-Proto`，请勿在可被局域网直连的部署上开启，否则客户端
可伪造登录限速用的 IP 与代理下载链接的协议。

## 验证

```powershell
cd backend
go vet ./...
go test ./internal/ipp
```

`go test ./internal/ipp` 覆盖多值编码、相邻同类型属性解析与真实响应常见结构。

## 接口概览

- `POST /api/auth/login`、`GET /api/auth/me`、`PATCH /api/auth/profile`
- `GET /api/printers`、`POST /api/files`、`POST /api/jobs`、`GET /api/jobs/events`
- `GET/POST /api/admin/groups`、`PUT /api/admin/groups/{id}/members|printers`
- `GET/POST /api/admin/printers`、`POST /api/admin/printers/{id}/endpoints`
- `POST /api/admin/discover`、`POST /api/admin/candidates/{id}/approve`
- `POST /api/agent/heartbeat|claim`、`POST /api/agent/jobs/{id}/start|result`

所有修改类浏览器请求都需要 `X-CSRF-Token`（登录/`/api/auth/me` 返回）。

## 开发说明

- 前端开发：`cd web; npm run dev`，Vite 代理到 `127.0.0.1:8080`。
- 前端构建产物输出到 `backend/cmd/cloudprint/ui`，随后编译进单一二进制。
- 数据表由 `backend/internal/database/migrations` 内嵌迁移自动创建。
- 不要把真实打印机 IP 写进代码或默认配置。
