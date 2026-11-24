# fnOS / NAS 部署（8080 端口）

仓库根目录的 `docker-compose.yml` 是在有外网的机器上多阶段构建用的。
fnOS 拉不动 `golang` / `node` 基础镜像，所以 NAS 上走这条链路：
本机交叉编译 → 上传 → NAS 上用 `deploy/Dockerfile.nas` 构建运行时镜像。

`deploy/nas/docker-compose.yml` 管理的实例对外发布在宿主机 **8080**，也就是
办公室书签里一直在用的地址。

原来的试点容器（容器名 `cloudprint`，数据在 `/vol1/1000/project/cloudprint`）
已**停止但未删除**，把 8080 让给本套服务。需要回滚时先停本套
`cloudprint-ui`，再 `docker start cloudprint` 即可；它的数据目录全程只读不动。

## 目录

```
/home/admin/nas-build/                  构建上下文（不挂进容器）
  cloudprint                            linux/amd64 二进制
  Dockerfile.nas
  cups-entrypoint.sh
  pantum/                               厂商 PPD、filter、/opt/pantum 库

/vol1/1000/project/cloudprint-ui/       运行时数据
  data/                                 SQLite、原件、生成的 PDF、预览图
  cups/                                 队列与 PPD 绑定
  spool/                                CUPS 任务状态与任务计数器
  cache/                                CUPS 属性缓存
```

`cups/` `spool/` `cache/` 三个目录都不能丢：丢了要重新加打印机，任务号也会从 1
重来，旧任务号可能匹配到新任务。

## 构建

构建要走本地 HTTP 代理（示例写 `<proxy-host>:7891`）。不加代理时 Debian 源可能
只有约 10 KB/s，装一次运行时包要十几分钟；加代理后两分钟出镜像。

```bash
cd /home/admin/nas-build
docker build -f Dockerfile.nas -t cloudprint:nas \
  --build-arg http_proxy=http://<proxy-host>:7891 \
  --build-arg https_proxy=http://<proxy-host>:7891 \
  --build-arg HTTP_PROXY=http://<proxy-host>:7891 \
  --build-arg HTTPS_PROXY=http://<proxy-host>:7891 \
  .
```

## 另存旧镜像

重建前先留一份回滚点：

```bash
docker tag cloudprint:nas cloudprint:nas-prev-$(date +%Y%m%d)
```

回滚就是把旧 tag 指回 `cloudprint:nas`，再 `up -d`。

## 运行

```bash
cp .env.example .env        # 至少设置 CLOUDPRINT_ADMIN_PASSWORD
cd deploy/nas && docker compose up -d
```

`deploy/nas/docker-compose.yml` 用的是 `image: cloudprint:nas`，没有 `build:`，
所以 `up -d` 不会触发重新构建，只会用现成的镜像重建容器。

## 验证

```bash
curl -s http://127.0.0.1:8080/readyz
# {"max_upload_mb":50,"office_formats":false,"pdf_preview":true,"status":"ready"}

docker exec cloudprint-ui lpstat -p
# printer ms6000nw is idle. enabled since ...
```

## 关于镜像里的字体

`Dockerfile.nas` 只装 `fonts-wqy-microhei`（5 MB）。删掉 Noto CJK 对 PDF 打印没有
影响：Ghostscript 光栅化用的是 PDF 内嵌字体，系统字体只在字体没内嵌时才参与替换。
同一份"字体未内嵌"的 PDF，新旧镜像的 `gs` 输出字节完全一致。

`fontconfig-config` 和 `libgs10-common` 会各自钉住一个字体包（`fonts-dejavu-core`
与 `fonts-urw-base35`，共约 18 MB），这是 Debian 的包依赖，删不掉，不是业务需求。

## 已知差异：内存占用

同一份代码，跑得久的容器 RSS 会明显更高。实测 3 天的实例约 68 MB，刚启动的约
27 MB，但 90 秒采样内两边都不增长 —— 是 Go 的堆在高水位后不急着归还，不是泄漏。
需要立刻降下来就重启容器。

## 管理员功能

后端 `/api/admin/*` 是完整的：用户、分组、配额、打印机、扫描、CUPS 队列都在
`backend/internal/webapi/server.go` 里注册了。`CLOUDPRINT_ANONYMOUS=1` 时，
没有会话的请求按**访客**处理：能看列表、上传和打印已有打印机，但 `/api/admin/*`
仍然只认管理员会话，所以游客删不掉打印机，也加不了新的。

前端已经恢复了一个轻量管理页：管理员登录后，右上角出现 **管理** 按钮，进入
`#/admin` 可以看到全部打印机（含已停用），停用/开放、删除，以及 **扫描添加打印机**
（`#/add`，扫描网段或手动填地址）。删除打印机时会尽力一并删除对应的 CUPS 队列，
避免留下还在占纸张的孤儿队列。

还没有前端入口的是 **成员与权限**（用户、分组、配额）和 **服务器打印驱动**
页面。页面不是"还没做"，而是在 `f233a97`（奔图驱动直连 + 免登录）里被删过。

要恢复得从该提交的父提交取回四个文件：

```bash
git checkout f233a97^ -- web/src/pages/Access.tsx web/src/pages/Settings.tsx \
  web/src/pages/Jobs.tsx web/src/pages/PrinterAdmin.tsx
```

但恢复后要重新接 `Shell.tsx` 的路由，并对照现在的 API（`handleListUsers`、
`handleListGroups` 等）逐个核对字段 —— 这些页面删于免登录改造之前，那之后
鉴权和 `currentUser` 的取法都变过。这是一块独立的功能工作，不在镜像瘦身范围内。

在此之前，用户、分组、配额的增删改可以直接调 `/api/admin/*`（先登录拿
`X-CSRF-Token`），或临时用 `GET /api/admin/cups/queues` 与
`GET /api/admin/cups/drivers` 核对队列实际绑定的驱动。
