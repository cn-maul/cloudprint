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
- 端点类型：`ipp`（设备原生支持 PDF 时直连）、`cups`（本机 CUPS 队列 + 厂商驱动）
- 发现与审批：mDNS、受控 CIDR 扫描、手工候选；发现结果不会自动启用
- 文件流水线：PDF 校验/页数、JPG/PNG 转 PDF（V1 不收 Office 文档）
- 打印任务：排队、打印机级串行、状态机、SSE 实时推送、取消、失败重试、
  已完成任务再打一份、设备进度（已出纸张数）、每日页数额度
- 每日额度按 NAS 本地时间的零点重置
- 纯 Go IPP：Get-Printer-Attributes 探测、Validate/Print-Job、Cancel-Job、
  直接提交设备格式数据
- Linux CUPS：探测队列状态、按机型自动匹配 PPD、在本机跑 PPD 里的厂商 filter，
  再把转换结果直接 IPP 发给设备（不经过 cupsd 的 spool 文件）
- 运维接口：打印机能力/选项、驱动与探测事件历史、一键诊断

## 重要边界

- 型号只提供**能力档案**，不据此假定能打印 PDF。是否可 IPP 直打 PDF 一律以端点
  实际探测结果为准。
- 服务端不会把 PDF 直接写到 RAW 9100。`socket://host:9100` 只能确认端口可达，不能
  验证设备能否解析 PDF。
- 已知奔图 MS6000NW 只接受 `image/urf` / `image/pwg-raster` / `application/octet-stream`，
  不接受 PDF。但实测它同样拒绝通用 PWG-Raster（返回 `0x0411
  document-format-error`），只吃 `ptm6500Filter` 产出的私有 GDI 流。因此这台机器
  必须用厂商 PPD，不能建无驱队列。
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
```

## NAS 部署（Docker）

```bash
cp .env.example .env
# 编辑 .env，至少设置一个强 CLOUDPRINT_ADMIN_PASSWORD
docker compose up -d --build
```

镜像包含 poppler（PDF 预览）与 CUPS；V1 只接受 PDF、JPG、PNG，Office 文档请先另存为 PDF。数据持久化在
`./data`，端口默认 `8080`。`./data/cups`（队列与 PPD）、`./data/spool` 与
`./data/cache`（CUPS 任务状态）都要保留，否则重建容器会丢队列、任务号也会从 1 重来。
反向代理时请把 `CLOUDPRINT_PUBLIC_URL` 设为外部访问地址，并只在可信代理后开启
`CLOUDPRINT_TRUST_PROXY_HEADERS=true`。

群晖/威联通若使用 Container Manager，直接导入 `docker-compose.yml`，把 `./data`
映射到 NAS 的共享文件夹，并给容器固定局域网 IP 或保留端口。

## 接入打印机（管理员，三步）

1. **打印机 → 搜索打印机**。系统同时搜索局域网设备，通常 10–40 秒完成。
2. 在结果里核对名字，点 **添加**。已添加过的打印机不会重复出现在搜索结果里；
   找不到时可以在 **手动添加** 里填打印机地址（例如 `192.168.2.91`）。
3. 如果设备不直接接收 PDF（例如奔图 MS6000NW），CloudPrint 优先使用服务器上已
   安装的厂商 PPD：队列的 PPD 决定了用哪个 filter 把光栅转成设备自己的格式。
   只有找不到厂商 PPD 时，才退回无驱队列（`-m everywhere`）。全程不需要其他
   电脑常开。

最后到 **成员与权限 → 新建分组**，勾选组员和打印机。普通用户只能看到并使用本组
授权的设备。

界面不使用 IPP、mDNS、端点等技术词。真实判定只看设备能否直接接收 PDF：不能直接
接收的机型会显示为“需要服务器驱动”，由 CUPS 队列出纸，避免任务提交成功却不出纸。
任务卡在“正在打印”时，界面会带上打印机自己上报的原因（缺纸、卡纸、盖板未关等）。
检测过程只读，不打印测试页，也不修改打印机设置。

## CUPS 驱动

Linux 上不需要 Windows 中继：CUPS 负责驱动和格式转换，CloudPrint 只上传、排队和
提交 PDF。设备原生支持 PDF 时走 `ipp` 直连；否则在服务器上建 `cups` 队列。

**厂商 PPD 优先。** 支持 IPP Everywhere / AirPrint 的机型（真正能吃 PWG-Raster
或 URF 的）用 `lpadmin -m everywhere` 建队列即可，不依赖任何厂商 filter，升级
系统也不会失效。但有些设备虽然宣称支持 PWG-Raster，实际只接受自家 filter 的
输出（奔图 MS6000NW 就是如此），这类机型必须装厂商 PPD，否则任务会以
`document-format-error` 失败且不出纸。

安装系统打印机驱动集合：

```bash
sudo apt install cups cups-client cups-filters printer-driver-all openprinting-ppds
```

奔图官方 Linux 驱动（含 337 个 PPD 与对应 filter），仅在老机型不支持无驱时才需要：

```bash
sudo ./deploy/install-pantum-driver.sh
```

脚本从奔图 CDN 下载官方包，把 PPD 装进 `/usr/share/cups/model/Pantum`、过滤器装进
`/usr/lib/cups/filter`，并补上可执行位（官方 deb 里部分 filter 没有 exec 位，cupsd
会报 `execv failed: Permission denied`）。之后在 **打印机 → 添加** 里重新接入，
系统会按设备上报的型号自动匹配 PPD。

在容器里跑厂商 filter 需要两件事，缺一不可：

- filter 必须可执行（官方 deb 里部分 filter 没有 exec 位）
- 运行服务的用户必须在 `lp` 组，否则读不到 `root:lp 0640` 的
  `/etc/cups/ppd/<queue>.ppd`

官方镜像已经处理这两点。奔图驱动自带的 CMS 库需要能正常加载，我们是把
`/opt/pantum/lib` 里的库软链到系统库目录后 `ldconfig` 解决的。

到 **成员与权限 → 服务器打印驱动** 可以查看已建队列、实际使用的驱动，以及服务器上
所有 PPD。队列名、驱动名和最近错误都在那里，出纸异常时先看这一页。

只复制 PPD 不复制过滤器是常见错误：`lp` 会收下任务，但打印机收不到可解析的数据，
表现为“提交成功、不出纸”。

## 配置项

| 变量 | 默认值 | 说明 |
|---|---|---|
| `CLOUDPRINT_ADDR` | `:8080` | 监听地址 |
| `CLOUDPRINT_DATA` | `./data` | 数据目录 |
| `CLOUDPRINT_DB` | `<data>/cloudprint.db` | SQLite 路径 |
| `CLOUDPRINT_PUBLIC_URL` | 空 | 外部访问地址，用于代理下载链接 |
| `CLOUDPRINT_ADMIN_USER` | `admin` | 初始管理员用户名 |
| `CLOUDPRINT_ADMIN_PASSWORD` | 随机 | 初始管理员密码，仅首次建库生效 |
| `CLOUDPRINT_ANONYMOUS` | `false` | 设为 `true` 后免登录，所有请求按管理员处理；仅限可信局域网 |
| `CLOUDPRINT_SESSION_HOURS` | `12` | 会话有效期 |
| `CLOUDPRINT_DAILY_QUOTA` | `50` | 新用户默认每日页数，0 为不限 |
| `CLOUDPRINT_MAX_UPLOAD_MB` | `50` | 单文件上限 |
| `CLOUDPRINT_RETENTION_DAYS` | `7` | 文件保留天数 |
| `CLOUDPRINT_ENABLE_MDNS` | `true` | 是否启用 mDNS 发现 |
| `CLOUDPRINT_SCAN_CIDRS` | 空 | 额外扫描网段，逗号分隔；留空则自动使用本机网卡所在网段 |
| `CLOUDPRINT_ALLOW_PRIVATE_SCAN` | `true` | 是否允许网段扫描 |
| `CLOUDPRINT_PDFTOPPM` | 自动查找 | `pdftoppm` 路径 |
| `CLOUDPRINT_CUPS_HOST` | `127.0.0.1:631` | 本机 CUPS 地址 |
| `CLOUDPRINT_PPD_DIRS` | 常见 CUPS 目录 | 额外 PPD 搜索目录，`:` 分隔 |
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
- `GET /api/printers`、`GET /api/printers/{id}`（含状态与队列，支持
  `limit`/`offset`/`status`）
- `GET /api/printers/{id}/options`：纸型、份数上限、单双面/彩色能力、设备支持的
  IPP 操作（前端据此决定哪些按钮可用）
- `GET /api/printers/{id}/diagnostics`：CUPS、PPD 里选中的 filter 及是否安装、
  Ghostscript、数据目录可写性、设备实时状态与最近事件
- `GET /api/printers/{id}/events`：驱动/探测历史，可按 `event` 过滤
- `POST /api/printers/{id}/control`：`pause` / `resume` / `purge`；设备未实现
  对应 IPP 操作时返回 501（奔图 MS6000NW 三种都不支持）
- `POST /api/files`、`POST /api/jobs`、`GET /api/jobs`、`GET /api/jobs/events`
- `POST /api/jobs/{id}/cancel`：打印中的任务直接对设备发 Cancel-Job
- `POST /api/jobs/{id}/retry`：失败/取消的任务复用原文件重排
- `POST /api/jobs/{id}/reprint`：已完成的任务再打一份
- `POST /api/printers/{id}/jobs/clear`：只清 `completed/failed/canceled`
- `GET/POST /api/admin/groups`、`PUT /api/admin/groups/{id}/members|printers`
- `GET/POST /api/admin/printers`、`POST /api/admin/printers/{id}/endpoints`
- `POST /api/admin/discover`、`POST /api/admin/candidates/{id}/approve`
- `GET /api/admin/cups/queues|drivers`、`POST /api/admin/cups/connect`、`DELETE /api/admin/cups/queues/{name}`

所有修改类浏览器请求都需要 `X-CSRF-Token`（登录/`/api/auth/me` 返回）。

## 开发说明

- 前端开发：`cd web; npm run dev`，Vite 代理到 `127.0.0.1:8080`。
- 前端构建产物输出到 `backend/cmd/cloudprint/ui`，随后编译进单一二进制。
- 数据表由 `backend/internal/database/migrations` 内嵌迁移自动创建。
- 不要把真实打印机 IP 写进代码或默认配置。
- CUPS 只在容器内监听 `127.0.0.1:631`，不要把它暴露到局域网。
