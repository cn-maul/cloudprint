# CloudPrint 项目审计报告

- 审计日期：2026-11-17
- 审计范围：`backend/`（Go 后端与 Windows 代理）、`web/`（React 前端）、`deploy/`（Docker 部署）、根目录脚本与配置
- 审计方式：通读源码 + 静态检查（`go vet`、`go test`、`npm run typecheck`）

## 验证结果

| 检查项 | 命令 | 结果 |
| --- | --- | --- |
| Go 静态检查 | `go vet ./...`（backend） | 通过 |
| Go 测试 | `go test ./...`（backend） | 通过；仅 `internal/ipp` 有测试，其余包 `[no test files]` |
| 前端类型检查 | `npm run typecheck`（web，`tsc -b`） | 通过 |

> 说明：静态检查通过只代表可编译、无明显语法/类型问题，不代表下列逻辑与安全缺陷不存在。

---

## 严重（P0）

### 1. 用户更新把明文密码写入审计日志
- 位置：`backend/internal/webapi/settings.go:123`
- 问题：`handleUpdateUser` 将包含 `Password string` 字段的请求结构体 `in` 直接传给 `s.Store.Audit(...)`，导致 `audit_logs.detail_json` 落库明文密码。
- 影响：审计日志、备份、数据库导出中均可读到明文密码；与 Argon2id 存储策略自相矛盾。
- 建议：审计 detail 只记录字段名白名单（如 changed: display_name/role/quota/is_active），绝不包含密码；对历史日志做清理。

### 2. 审批发现候选时必然 panic（空指针解引用）
- 位置：`backend/internal/webapi/discovery.go:231-248`
- 问题：`CreateEndpoint` 返回 `store.ErrConflict` 时 `ep == nil`；255 行判断用 `err != store.ErrConflict`（非 `errors.Is`）后继续执行，`discovery.go:241` 的 `ep.ID` 解引用触发 panic，被 `Recoverer` 兜底成 500。
- 影响：重复审批/端点已存在时接口 500；且 `SetCandidateStatus(... "approved")` 已先执行，必要时还新建了打印机，形成半成品脏数据。
- 建议：使用 `errors.Is(err, store.ErrConflict)`；冲突时取出已存在端点（`GetEndpointByDedupe`），审计与探测逻辑包在 `if ep != nil` 内。

### 3. 删除打印机/代理会被外键约束卡死
- 位置：`backend/internal/database/migrations/001_init.sql:129,131`；`backend/internal/webapi/printers.go`（`handleDeletePrinter`）；`backend/internal/webapi/agents.go`（`handleDeleteAgent`）
- 问题：`print_jobs.printer_id`、`print_jobs.agent_id` 引用外键但未声明 `ON DELETE` 动作；只要该打印机/代理处理过任务，删除即触发 FK 约束错误 → 500。
- 影响：前端确认文案称"已提交的打印任务记录会保留"，实际无法删除；运维无法下线旧设备。
- 建议：迁移改为 `ON DELETE SET NULL`（保留历史任务），或在删除时事务内先置空引用；或改为软删除。

### 4. 卡在 `printing` 的任务永不回收，打印机会被永久堵死
- 位置：`backend/internal/store/jobs.go:307`（`RequeueExpiredClaims`）；`backend/internal/printer/executor.go:134`（`MarkPrinting`）
- 问题：`RequeueExpiredClaims` 只回收 `claimed` 状态，不处理 `printing`。代理打印中途崩溃或服务器重启后，任务停在 `printing`，`HasActiveJob` 恒真，Worker 不再向该打印机派发任何任务。
- 影响：单打印机级永久故障，需手工改数据库。
- 建议：为 `printing` 也设定租约超时（如 15 分钟无心跳）回收为 `queued` 或标记 `failed`，并记录审计。

### 5. Docker 部署缺少 Windows 代理安装包
- 位置：`deploy/Dockerfile:18,27`；`backend/internal/webapi/server.go:156`（`handleAgentBinary`）
- 问题：镜像只构建 `./cmd/cloudprint`，最终仅复制 `/app/cloudprint`；`handleAgentBinary` 从可执行文件同目录查找 `cloudprint-agent.exe`，Docker 镜像中不存在。
- 影响：官方 Docker 部署下 `/downloads/cloudprint-agent.exe` 必然 404，Windows 代理接入流程不可用。
- 建议：构建阶段同时 `GOOS=windows go build ./cmd/agent`，产物复制进镜像；或在文档中明确代理包需另行分发。

---

## 中（P1）

### 6. 可把最后一个管理员降级或停用
- 位置：`backend/internal/webapi/settings.go:92`（`handleUpdateUser`）
- 问题：`DeleteUser` 有"最后一个管理员"保护，但 `UpdateUser` 允许修改 `role` / `is_active`，无同等校验，可把自己或唯一管理员降级/停用导致系统锁死。
- 建议：在 Store 层统一校验"至少保留一个启用的 admin"。

### 7. 改密/重置密码后旧会话仍有效
- 位置：`backend/internal/webapi/auth.go:101`（`handleChangePassword`）；`settings.go:118`（管理员重置密码）
- 问题：只删当前会话，其它设备会话保持有效；管理员重置他人密码也不吊销目标用户会话。
- 建议：改密时删除该用户全部会话（`DeleteUserSessions`）。

### 8. 每日额度可并发超发
- 位置：`backend/internal/webapi/jobs.go`（`handleCreateJob`）
- 问题：先 `PagesToday` 判额度、后 `CreateJob`，两步非原子，并发提交可突破每日额度。
- 建议：在单一事务/带条件的 INSERT 中完成额度校验与建单，或用数据库行锁串行化。

### 9. 代理领取任务不校验启用状态
- 位置：`backend/internal/webapi/agents.go:204`（`handleAgentClaim`）
- 问题：只校验 queued + 端点匹配，未校验端点/打印机是否停用；停用后遗留的 queued 任务仍会被代理领走执行。
- 建议：领取前检查 `printer_endpoints.is_enabled` 与打印机状态；停用时把遗留 queued 任务置为取消。

### 10. 上传无磁盘配额/速率限制
- 位置：`backend/internal/webapi/files.go`（`handleUpload`）
- 问题：任何登录用户可无限上传（单文件 50MB 上限之外无总量限制），也无速率限制。
- 建议：增加逐用户/全局磁盘配额与上传频率限制，超限返回可读错误。

### 11. 删除用户不清理物理文件
- 位置：`backend/internal/store/users.go`（`DeleteUser`）；`backend/internal/files/pipeline.go`
- 问题：DB 行级联删除 job/file 元数据，但 `originals/`、`pdf/`、`previews/` 下物理文件残留，长期占用磁盘。
- 建议：删除前收集存储路径，事务提交后异步清理；或由过期清理统一兜底孤儿文件。

### 12. SSE 被全局 10 分钟超时切断
- 位置：`backend/internal/webapi/server.go:68`（`middleware.Timeout(10 * time.Minute)`）
- 问题：全局超时作用于 `/api/jobs/events`，SSE 每 10 分钟被取消；EventSource 会自动重连，但断连窗口内事件会丢失。
- 建议：对 SSE 路由豁免 Timeout，或在 handler 内使用无 deadline 的独立 context。

### 13. 登录限速表无上限
- 位置：`backend/internal/auth/limiter.go`
- 问题：`entries` map 永不淘汰，key 为 `IP|username`，攻击者可制造海量 key 撑爆内存。
- 建议：加入定期清理/容量上限/LRU。

### 14. 登录存在用户枚举时序差异
- 位置：`backend/internal/webapi/auth.go`（`handleLogin`）
- 问题：用户不存在时跳过 Argon2，响应时间明显短于存在用户。
- 建议：用户不存在时也执行一次等价的 dummy Argon2 校验。

### 15. ipps 关闭 TLS 证书校验
- 位置：`backend/internal/ipp/client.go:60`（`InsecureSkipVerify: true`）
- 问题：`ipps://` 打印机连接不校验证书，可被中间人劫持。
- 建议：默认校验，提供"信任自签名证书"的显式开关并按指纹固定。

---

## 低（P2）

16. **额度 0 语义冲突**：前端标注"0 表示不限"，但 `settings.go:76` 会把新建用户的 0 强制改为 `DefaultDailyQuota`；负额度又被视为不限。需统一语义并做范围校验。
17. **发现计数虚高**：`FinishDiscoveryRun` 的 `found` 包含 ignored/approved 的 upsert 项。
18. **子网扫描漏判**：`backend/internal/printer/discovery/scan.go` 的 `hosts()` 无条件跳过 `ip[3]==255`，小于 /24 的网段会误跳合法地址。
19. **Host 头注入面**：`agentSetupScript` 用 `r.Host` 拼接 PowerShell 脚本与下载地址。
20. **代理名称后缀熵低**：`auth.RandomToken(2)` 约 16 bit，存在唯一性冲突概率。
21. **配置弱兜底**：`config` 的 `randomPassword` 有取模偏置；`crypto/rand` 失败时兜底固定串 `change-me-1234`；启动日志会打印初始密码。
22. **安全头缺失**：`securityHeaders` 无 CSP / HSTS；CSRF 用 `!=` 非常量时间比较；会话 token 明文入库。
23. **权限信息范围过大**：`adminDashboard` 与 `handleDashboard` 实现完全相同，普通用户能看到全站任务状态计数与打印机总数。
24. **下载文件名编码**：使用 `url.PathEscape` 而非 RFC5987 `filename*`，中文/特殊字符文件名在部分客户端异常。
25. **`must_change_password` 未生效**：全站无强制改密逻辑，管理员重置密码不会要求用户下次登录修改。
26. **IPP 解析器可被异常 tag 卡死**：`parseValue` 对 `0x10/0x12/0x13` 等 tag 不前进位置，组循环可能原地打转，探测 goroutine 死循环占 CPU。
27. **转换/解码无资源上限**：LibreOffice 转 PDF 与图片解码无像素/内存/时长硬上限，存在 DoS 与解析器攻击面。

---

## 代码异味 / 死代码

- `backend/internal/webapi/auth_bridge.go`：整文件死代码（`toSession`、`var _ = context.Background`）。
- `backend/internal/database/seed.go`：`tokenHash`、`normalizeName` 未使用，靠 `var _ = uuid.NewString` / `var _ = time.Now` 压制 unused import。
- 未被引用的符号（仅定义处出现）：`SavePDFCopy`、`SetJobPDF`、`GetEndpointByDedupe`、`ImageDimensions`、`FormatDuration`、`ValidateJob`、`EncodeInt`、`StatusText`、`SortedAttrNames`、`ActiveEndpointForAgent`。
- `handleCreateJob` 对同一 `PrinterID` 调用 `GetPrinter` 两次。
- `ListPrinters` / `ListGroups` 存在 N+1 查询；`GetAgent` 每次调用 `ListAgents` 全量扫描。

---

## 前端

- `web/src/components/Shell.tsx`：文案"今日还可打印 X 页"实际使用 `daily_page_quota`（总额度）而非剩余额度，误导用户。
- `web/src/pages/Access.tsx`：HelperPanel 脚本兜底用 `window.location.origin`，从 localhost 打开会生成同事无法访问的地址；`agents[0]` 被当作"最新"，但后端按 name 排序而非在线优先。
- 用户管理：后端支持改用户名/额度/角色/重置密码，前端 UserPanel 无对应入口；管理员改额度需用户重新登录才生效。
- `web/src/pages/Jobs.tsx`：会话过期（401）后 EventSource 无限重连。
- `web/src/pages/Login.tsx`：用户名默认预填 `admin`。

---

## 部署 / 配置

- `go.mod` 声明 `go 1.27.4`，前端 `react 19.3.1` / `vite 8.3.8`，版本很新，需确认与 CI 及其他环境一致且可稳定构建。
- `.env.example` 的默认占位密码未被强制替换（仅靠 `HashPassword` 的 8 位下限）。
- `docker-compose.yml` 以 `./data:/data` 绑定挂载；mDNS 需 host network，注释已提示。
- `deploy/Dockerfile` 以非 root `cloudprint` 运行，含 tzdata / libreoffice / fonts-noto-cjk / poppler-utils / wget，健康检查齐全（但见 P0-5 缺代理包）。
- `Start-CloudPrint.ps1` 中文输出显示为乱码，疑似文件非 UTF-8(BOM) 编码。

---

## 建议修复顺序

1. 先修 P0：明文密码入审计、审批 panic、删除 FK 卡死、`printing` 任务永不回收、Docker 缺代理包。
2. 再修 P1：最后管理员保护、会话吊销、额度原子性、代理领取校验、磁盘配额、残留文件清理。
3. 最后处理 P2、死代码与前端文案/入口。
