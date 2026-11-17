import {useCallback, useEffect, useMemo, useState} from "react";
import {Button, Chip, Input, ListBox, Modal, Select} from "@heroui/react";
import {api, type Printer, type User, type UserGroup} from "../lib/api";
import {notifyError, notifySuccess} from "../lib/toast";
import {EmptyBox, Field, PageHeader, SectionCard} from "../components/ui";

type Tab = "groups" | "users" | "helpers";

export default function AccessPage({user}: {user: User}) {
  const [tab, setTab] = useState<Tab>("groups");
  if (user.role !== "admin") return null;
  const tabs: {key: Tab; label: string}[] = [
    {key: "groups", label: "分组与权限"},
    {key: "users", label: "账号"},
    {key: "helpers", label: "帮忙转发的电脑"},
  ];
  return (
    <div className="space-y-5">
      <PageHeader
        subtitle="给同事分组，决定谁可以用哪几台打印机。"
        title="成员与权限"
      />
      <div className="flex flex-wrap gap-1 rounded-xl border border-separator/60 bg-surface/60 p-1">
        {tabs.map((t) => (
          <button
            className={`rounded-lg px-3 py-1.5 text-sm transition ${
              tab === t.key ? "bg-accent/12 font-medium text-accent" : "text-muted hover:text-foreground"
            }`}
            key={t.key}
            onClick={() => setTab(t.key)}
            type="button"
          >
            {t.label}
          </button>
        ))}
      </div>
      {tab === "groups" ? <GroupPanel /> : null}
      {tab === "users" ? <UserPanel current={user} /> : null}
      {tab === "helpers" ? <HelperPanel /> : null}
    </div>
  );
}

function GroupPanel() {
  const [groups, setGroups] = useState<UserGroup[]>([]);
  const [users, setUsers] = useState<User[]>([]);
  const [printers, setPrinters] = useState<Printer[]>([]);
  const [open, setOpen] = useState(false);
  const [editing, setEditing] = useState<UserGroup | null>(null);
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [memberIDs, setMemberIDs] = useState<string[]>([]);
  const [printerIDs, setPrinterIDs] = useState<string[]>([]);
  const [busy, setBusy] = useState(false);

  const load = useCallback(async () => {
    try {
      const [g, u, p] = await Promise.all([
        api<{groups: UserGroup[]}>("/api/admin/groups"),
        api<{users: User[]}>("/api/admin/users"),
        api<{printers: Printer[]}>("/api/admin/printers"),
      ]);
      setGroups(g.groups);
      setUsers(u.users);
      setPrinters((p.printers || []).filter((x) => x.is_enabled));
    } catch (e) {
      notifyError("载入失败", e instanceof Error ? e.message : "");
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  function openCreate() {
    setEditing(null);
    setName("");
    setDescription("");
    setMemberIDs([]);
    setPrinterIDs([]);
    setOpen(true);
  }

  function openEdit(g: UserGroup) {
    setEditing(g);
    setName(g.name);
    setDescription(g.description);
    setMemberIDs(g.members?.map((m) => m.id) ?? []);
    setPrinterIDs(g.printer_ids ?? []);
    setOpen(true);
  }

  async function save() {
    if (!name.trim()) return;
    setBusy(true);
    try {
      let id = editing?.id;
      if (id) {
        await api(`/api/admin/groups/${id}`, {
          method: "PATCH",
          body: JSON.stringify({name: name.trim(), description}),
        });
      } else {
        const created = await api<UserGroup>("/api/admin/groups", {
          method: "POST",
          body: JSON.stringify({name: name.trim(), description}),
        });
        id = created.id;
      }
      await Promise.all([
        api(`/api/admin/groups/${id}/members`, {
          method: "PUT",
          body: JSON.stringify({user_ids: memberIDs}),
        }),
        api(`/api/admin/groups/${id}/printers`, {
          method: "PUT",
          body: JSON.stringify({printer_ids: printerIDs}),
        }),
      ]);
      notifySuccess(editing ? "分组已更新" : "分组已创建", `${memberIDs.length} 人 · ${printerIDs.length} 台打印机`);
      setOpen(false);
      void load();
    } catch (e) {
      notifyError("保存失败", e instanceof Error ? e.message : "");
    } finally {
      setBusy(false);
    }
  }

  async function remove(g: UserGroup) {
    if (!window.confirm(`删除分组“${g.name}”？组员帐号不会删除。`)) return;
    try {
      await api(`/api/admin/groups/${g.id}`, {method: "DELETE"});
      notifySuccess("已删除", g.name);
      void load();
    } catch (e) {
      notifyError("删除失败", e instanceof Error ? e.message : "");
    }
  }

  function toggle(list: string[], id: string) {
    return list.includes(id) ? list.filter((x) => x !== id) : [...list, id];
  }

  const ungrouped = useMemo(() => {
    const members = new Set<string>();
    for (const g of groups) for (const m of g.members ?? []) members.add(m.id);
    return users.filter((u) => !members.has(u.id) && u.role !== "admin");
  }, [groups, users]);

  return (
    <div className="space-y-4">
      <SectionCard
        actions={
          <>
            <Button size="sm" variant="secondary" onPress={() => void load()}>
              刷新
            </Button>
            <Button size="sm" onPress={openCreate}>
              新建分组
            </Button>
          </>
        }
        description="一个人可以属于多个组，权限自动合并。管理员不需要分组也能用所有打印机。"
        title="分组"
      >
        {groups.length === 0 ? (
          <EmptyBox
            action={
              <Button size="sm" onPress={openCreate}>
                新建第一个分组
              </Button>
            }
            description="例如“教务处”“高三年级组”，各自授权不同的打印机。"
            title="还没有分组"
          />
        ) : (
          <div className="space-y-2">
            {groups.map((g) => {
              const granted = printers.filter((p) => (g.printer_ids ?? []).includes(p.id));
              return (
                <div className="rounded-xl border border-separator/60 bg-surface/50 p-4" key={g.id}>
                  <div className="flex flex-wrap items-center gap-2">
                    <span className="font-medium">{g.name}</span>
                    <Chip size="sm" variant="soft">
                      {g.member_count} 人
                    </Chip>
                    <Chip color={granted.length ? "success" : "warning"} size="sm" variant="soft">
                      {granted.length ? `${granted.length} 台打印机` : "还没分配打印机"}
                    </Chip>
                    <span className="ml-auto flex gap-2">
                      <Button size="sm" variant="ghost" onPress={() => openEdit(g)}>
                        编辑
                      </Button>
                      <Button size="sm" variant="danger-soft" onPress={() => void remove(g)}>
                        删除
                      </Button>
                    </span>
                  </div>
                  {g.description ? <div className="mt-1 text-xs text-muted">{g.description}</div> : null}
                  <div className="mt-2 flex flex-wrap gap-1.5">
                    {granted.length ? (
                      granted.map((p) => (
                        <Chip key={p.id} size="sm" variant="secondary">
                          {p.display_name}
                        </Chip>
                      ))
                    ) : (
                      <span className="text-xs text-warning">组员现在还无法打印，请编辑并勾选打印机。</span>
                    )}
                  </div>
                </div>
              );
            })}
          </div>
        )}
        {ungrouped.length > 0 ? (
          <div className="mt-4 rounded-xl border border-warning/30 bg-warning/8 p-3 text-xs leading-5 text-warning">
            {ungrouped.length} 个账号还没有加入任何分组，他们登录后看不到打印机：
            {ungrouped.map((u) => u.display_name).join("、")}
          </div>
        ) : null}
      </SectionCard>

      <Modal isOpen={open} onOpenChange={setOpen}>
        <Modal.Backdrop>
          <Modal.Container size="lg">
            <Modal.Dialog className="sm:max-w-[720px]">
              <Modal.CloseTrigger />
              <Modal.Header>
                <Modal.Heading>{editing ? "编辑分组" : "新建分组"}</Modal.Heading>
              </Modal.Header>
              <Modal.Body className="space-y-4">
                <div className="grid gap-3 sm:grid-cols-2">
                  <Field label="分组名称">
                    <Input fullWidth placeholder="例如：教务处" value={name} onChange={(e) => setName(e.target.value)} />
                  </Field>
                  <Field label="说明（可选）">
                    <Input fullWidth placeholder="这个组负责什么" value={description} onChange={(e) => setDescription(e.target.value)} />
                  </Field>
                </div>
                <div className="grid gap-4 md:grid-cols-2">
                  <div>
                    <div className="mb-2 flex items-center justify-between">
                      <span className="text-sm font-medium">组员</span>
                      <span className="text-xs text-muted">已选 {memberIDs.length} 人</span>
                    </div>
                    <div className="scroll-thin max-h-64 space-y-1 overflow-y-auto rounded-xl border border-separator/60 p-2">
                      {users.length === 0 ? (
                        <p className="px-2 py-4 text-xs text-muted">还没有账号，请先在“账号”里新建。</p>
                      ) : (
                        users.map((u) => (
                          <button
                            className={`flex w-full items-center gap-2 rounded-lg px-2 py-1.5 text-left text-sm transition ${
                              memberIDs.includes(u.id) ? "bg-accent/10 text-accent" : "hover:bg-default/60"
                            }`}
                            key={u.id}
                            onClick={() => setMemberIDs((v) => toggle(v, u.id))}
                            type="button"
                          >
                            <span className={`grid size-4 place-items-center rounded border ${memberIDs.includes(u.id) ? "border-accent bg-accent text-accent-foreground" : "border-separator"}`}>
                              {memberIDs.includes(u.id) ? <CheckGlyph /> : null}
                            </span>
                            <span className="min-w-0 flex-1 truncate">
                              {u.display_name} <span className="text-xs text-muted">@{u.username}</span>
                            </span>
                            {u.role === "admin" ? (
                              <Chip size="sm" variant="soft">
                                管理员
                              </Chip>
                            ) : null}
                          </button>
                        ))
                      )}
                    </div>
                  </div>
                  <div>
                    <div className="mb-2 flex items-center justify-between">
                      <span className="text-sm font-medium">这些组员可以用的打印机</span>
                      <span className="text-xs text-muted">已选 {printerIDs.length} 台</span>
                    </div>
                    <div className="scroll-thin max-h-64 space-y-1 overflow-y-auto rounded-xl border border-separator/60 p-2">
                      {printers.length === 0 ? (
                        <p className="px-2 py-4 text-xs text-muted">还没有已开放的打印机，请先去“打印机管理”添加。</p>
                      ) : (
                        printers.map((p) => (
                          <button
                            className={`flex w-full items-center gap-2 rounded-lg px-2 py-1.5 text-left text-sm transition ${
                              printerIDs.includes(p.id) ? "bg-accent/10 text-accent" : "hover:bg-default/60"
                            }`}
                            key={p.id}
                            onClick={() => setPrinterIDs((v) => toggle(v, p.id))}
                            type="button"
                          >
                            <span className={`grid size-4 place-items-center rounded border ${printerIDs.includes(p.id) ? "border-accent bg-accent text-accent-foreground" : "border-separator"}`}>
                              {printerIDs.includes(p.id) ? <CheckGlyph /> : null}
                            </span>
                            <span className="min-w-0 flex-1 truncate">
                              {p.display_name}
                              {p.location ? <span className="ml-1 text-xs text-muted">{p.location}</span> : null}
                            </span>
                          </button>
                        ))
                      )}
                    </div>
                  </div>
                </div>
              </Modal.Body>
              <Modal.Footer>
                <Button slot="close" variant="secondary">
                  取消
                </Button>
                <Button isDisabled={busy || !name.trim()} onPress={save}>
                  {busy ? "保存中…" : "保存"}
                </Button>
              </Modal.Footer>
            </Modal.Dialog>
          </Modal.Container>
        </Modal.Backdrop>
      </Modal>
    </div>
  );
}

function UserPanel({current}: {current: User}) {
  const [users, setUsers] = useState<User[]>([]);
  const [open, setOpen] = useState(false);
  const [form, setForm] = useState({username: "", display_name: "", password: "", role: "user", quota: 50});
  const [busy, setBusy] = useState(false);

  const load = useCallback(async () => {
    try {
      const r = await api<{users: User[]}>("/api/admin/users");
      setUsers(r.users);
    } catch (e) {
      notifyError("载入账号失败", e instanceof Error ? e.message : "");
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  async function create() {
    setBusy(true);
    try {
      await api("/api/admin/users", {
        method: "POST",
        body: JSON.stringify({
          username: form.username.trim(),
          display_name: form.display_name.trim() || form.username.trim(),
          password: form.password,
          role: form.role,
          daily_page_quota: form.quota,
        }),
      });
      notifySuccess("账号已创建", "别忘了把它加入分组，否则看不到打印机");
      setOpen(false);
      setForm({username: "", display_name: "", password: "", role: "user", quota: 50});
      void load();
    } catch (e) {
      notifyError("创建失败", e instanceof Error ? e.message : "");
    } finally {
      setBusy(false);
    }
  }

  async function toggle(u: User) {
    try {
      await api(`/api/admin/users/${u.id}`, {
        method: "PATCH",
        body: JSON.stringify({is_active: !u.is_active}),
      });
      void load();
    } catch (e) {
      notifyError("修改失败", e instanceof Error ? e.message : "");
    }
  }

  async function remove(u: User) {
    if (!window.confirm(`确定删除账号“${u.display_name}”（@${u.username}）？\n该账号的会话和打印记录会一起删除，无法恢复。`)) {
      return;
    }
    try {
      await api(`/api/admin/users/${u.id}`, {method: "DELETE"});
      notifySuccess("账号已删除", u.display_name);
      void load();
    } catch (e) {
      notifyError("删除失败", e instanceof Error ? e.message : "");
    }
  }

  return (
    <SectionCard
      actions={
        <Button size="sm" onPress={() => setOpen(true)}>
          新建账号
        </Button>
      }
      description="给每位同事一个账号。每日页数填 0 表示不限。"
      title={`账号 · ${users.length}`}
    >
      {users.length === 0 ? (
        <EmptyBox
          action={
            <Button size="sm" onPress={() => setOpen(true)}>
              新建第一个账号
            </Button>
          }
          description="同事用这个账号登录后上传文件打印。"
          title="还没有账号"
        />
      ) : (
        <div className="divide-y divide-separator/50">
          {users.map((u) => (
            <div className="flex flex-wrap items-center gap-3 py-3" key={u.id}>
              <div className="grid size-9 place-items-center rounded-full bg-accent/12 text-sm font-semibold text-accent">
                {(u.display_name || u.username).slice(0, 1).toUpperCase()}
              </div>
              <div className="min-w-0 flex-1">
                <div className="truncate text-sm font-medium">{u.display_name}</div>
                <div className="text-[11px] text-muted">
                  @{u.username} · {u.role === "admin" ? "管理员" : u.daily_page_quota > 0 ? `每天 ${u.daily_page_quota} 页` : "不限页数"}
                </div>
              </div>
              <Chip color={u.is_active ? "success" : "default"} size="sm" variant="soft">
                {u.is_active ? "正常" : "已停用"}
              </Chip>
              {u.id === current.id ? (
                <span className="text-xs text-muted">当前登录</span>
              ) : (
                <>
                  <Button size="sm" variant="ghost" onPress={() => void toggle(u)}>
                    {u.is_active ? "停用" : "启用"}
                  </Button>
                  <Button size="sm" variant="danger-soft" onPress={() => void remove(u)}>
                    删除
                  </Button>
                </>
              )}
            </div>
          ))}
        </div>
      )}
      <Modal isOpen={open} onOpenChange={setOpen}>
        <Modal.Backdrop>
          <Modal.Container>
            <Modal.Dialog className="sm:max-w-[520px]">
              <Modal.CloseTrigger />
              <Modal.Header>
                <Modal.Heading>新建账号</Modal.Heading>
              </Modal.Header>
              <Modal.Body className="space-y-3">
                <div className="grid gap-3 sm:grid-cols-2">
                  <Field label="登录名">
                    <Input fullWidth placeholder="例如：zhangsan" value={form.username} onChange={(e) => setForm({...form, username: e.target.value})} />
                  </Field>
                  <Field label="姓名">
                    <Input fullWidth placeholder="例如：张三" value={form.display_name} onChange={(e) => setForm({...form, display_name: e.target.value})} />
                  </Field>
                </div>
                <Field label="初始密码" hint="至少 8 个字符，登录后可自行修改">
                  <Input fullWidth type="password" value={form.password} onChange={(e) => setForm({...form, password: e.target.value})} />
                </Field>
                <div className="grid grid-cols-2 gap-3">
                  <Field label="角色">
                    <Select fullWidth selectedKey={form.role} onSelectionChange={(k) => setForm({...form, role: String(k)})}>
                      <Select.Trigger>
                        <Select.Value />
                        <Select.Indicator />
                      </Select.Trigger>
                      <Select.Popover>
                        <ListBox>
                          <ListBox.Item id="user" textValue="普通用户">
                            普通用户
                            <ListBox.ItemIndicator />
                          </ListBox.Item>
                          <ListBox.Item id="admin" textValue="管理员">
                            管理员
                            <ListBox.ItemIndicator />
                          </ListBox.Item>
                        </ListBox>
                      </Select.Popover>
                    </Select>
                  </Field>
                  <Field label="每天可打印页数" hint="0 表示不限">
                    <Input
                      fullWidth
                      type="number"
                      value={String(form.quota)}
                      onChange={(e) => setForm({...form, quota: Number(e.target.value)})}
                    />
                  </Field>
                </div>
              </Modal.Body>
              <Modal.Footer>
                <Button slot="close" variant="secondary">
                  取消
                </Button>
                <Button
                  isDisabled={busy || !form.username.trim() || form.password.length < 8}
                  onPress={create}
                >
                  {busy ? "创建中…" : "创建账号"}
                </Button>
              </Modal.Footer>
            </Modal.Dialog>
          </Modal.Container>
        </Modal.Backdrop>
      </Modal>
    </SectionCard>
  );
}

function HelperPanel() {
  const [agents, setAgents] = useState<import("../lib/api").Agent[]>([]);
  const [open, setOpen] = useState(false);
  const [name, setName] = useState("");
  const [printerID, setPrinterID] = useState("");
  const [queue, setQueue] = useState("");
  const [printers, setPrinters] = useState<Printer[]>([]);
  const [token, setToken] = useState("");
  const [created, setCreated] = useState<{id: string; script: string} | null>(null);
  const [busy, setBusy] = useState(false);

  const load = useCallback(async () => {
    try {
      const [r, p] = await Promise.all([
        api<{agents: import("../lib/api").Agent[]}>("/api/admin/agents"),
        api<{printers: Printer[]}>("/api/admin/printers"),
      ]);
      setAgents(r.agents);
      setPrinters(p.printers ?? []);
    } catch (e) {
      notifyError("载入转发电脑失败", e instanceof Error ? e.message : "");
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  async function create() {
    setBusy(true);
    try {
      const r = await api<{
        agent: import("../lib/api").Agent;
        token: string;
        endpoint_id: string;
      }>("/api/admin/agents", {
        method: "POST",
        body: JSON.stringify({
          name: name.trim(),
          printer_id: printerID,
          queue_name: queue.trim(),
        }),
      });
      setToken(r.token);
      setCreated({id: r.endpoint_id, script: script(r.token, r.endpoint_id)});
      void load();
    } catch (e) {
      notifyError("创建失败", e instanceof Error ? e.message : "");
    } finally {
      setBusy(false);
    }
  }

  async function remove(id: string) {
    if (!window.confirm("移除这台帮忙转发的电脑？它负责的打印机会暂时不能打印。")) return;
    try {
      await api(`/api/admin/agents/${id}`, {method: "DELETE"});
      void load();
    } catch (e) {
      notifyError("删除失败", e instanceof Error ? e.message : "");
    }
  }

  async function reissue(a: import("../lib/api").Agent) {
    if (!window.confirm("重新生成安装命令？旧密钥会立即作废，需要在那台 Windows 电脑上重新运行一次。")) return;
    try {
      const r = await api<{token: string; endpoint_id: string; setup_script: string; message: string}>(
        `/api/admin/agents/${a.id}/token`,
        {method: "POST"},
      );
      if (!r.setup_script) {
        throw new Error("服务器没有返回安装命令，请刷新后重试");
      }
      setCreated({id: r.endpoint_id || a.endpoint_ids?.[0] || a.id, script: r.setup_script});
      setToken(r.token);
      setOpen(true);
      notifySuccess("新命令已生成", "旧密钥已作废");
    } catch (e) {
      notifyError("生成失败", e instanceof Error ? e.message : "");
    }
  }

  function script(t: string, id: string, base = window.location.origin) {
    const tail = id.slice(-6);
    return `$ErrorActionPreference = "Stop"
$dir = Join-Path $env:LOCALAPPDATA "CloudPrintAgent"
New-Item -ItemType Directory -Force -Path $dir | Out-Null
$exe = Join-Path $dir "cloudprint-agent.exe"
Invoke-WebRequest "${base}/downloads/cloudprint-agent.exe" -OutFile $exe
$arg = '-server "${base}" -token "${t}" -endpoint "${id}"'
$action = New-ScheduledTaskAction -Execute $exe -Argument $arg
$trigger = New-ScheduledTaskTrigger -AtLogOn
Register-ScheduledTask -TaskName 'CloudPrintAgent-${tail}' -Action $action -Trigger $trigger -Description 'CloudPrint 打印转发' -Force | Out-Null
Start-ScheduledTask -TaskName 'CloudPrintAgent-${tail}'
Write-Host '打印转发已安装并开始运行。' -ForegroundColor Green`;
  }

  const latest = agents[0];
  const active = agents.filter((a) => a.online).length;
  const boundPrinters = printers.filter((p) => p.is_enabled);

  return (
    <div className="space-y-4">
      <SectionCard
        actions={
          <>
            <Button size="sm" variant="secondary" onPress={() => void load()}>
              刷新
            </Button>
            <Button
              size="sm"
              onPress={() => {
                setOpen(true);
                setToken("");
                setCreated(null);
                setName("");
                setQueue("");
                setPrinterID(boundPrinters[0]?.id ?? "");
              }}
            >
              添加转发电脑
            </Button>
          </>
        }
        description="有些打印机需要一台装着驱动的 Windows 电脑常开转发。一般添加打印机时会自动创建，不需要在这里手动加。"
        title={`帮忙转发的电脑 · ${active}/${agents.length} 台在线`}
      >
        {agents.length === 0 ? (
          <EmptyBox
            description="在“打印机管理”里添加打印机时，系统会自动判断是否需要、并生成安装命令。"
            title="还没有需要转发的打印机"
          />
        ) : (
          <div className="space-y-2">
            {agents.map((a) => (
              <div className="flex flex-wrap items-center gap-2 rounded-xl border border-separator/60 bg-surface/50 p-3" key={a.id}>
                <Chip color={a.online ? "success" : "default"} size="sm" variant="soft">
                  {a.online ? "在线" : "离线"}
                </Chip>
                <span className="font-medium">{prettyAgentName(a.name)}</span>
                <span className="text-[11px] text-muted">
                  {a.printer_ids?.length
                    ? `负责：${a.printer_ids.map((id) => printers.find((p) => p.id === id)?.display_name || id).join("、")}`
                    : "还未绑定打印机"}
                </span>
                <span className="ml-auto text-[11px] text-muted">
                  {a.last_seen_at ? `上次连接 ${new Date(a.last_seen_at).toLocaleString("zh-CN")}` : "从未连接"}
                </span>
                <span className="flex gap-2">
                  {a.printer_ids?.length ? (
                    <Button size="sm" variant="ghost" onPress={() => void reissue(a)}>
                      重新生成安装命令
                    </Button>
                  ) : null}
                  <Button size="sm" variant="danger-soft" onPress={() => void remove(a.id)}>
                    删除
                  </Button>
                </span>
              </div>
            ))}
          </div>
        )}
        {latest && latest.online ? (
          <p className="mt-3 text-xs text-muted">显示“在线”说明那台 Windows 电脑正在待命。</p>
        ) : null}
      </SectionCard>

      <SectionCard title="怎么让那台电脑开始帮忙">
        <ol className="space-y-2 text-sm leading-6 text-muted">
          <li>1. 在“打印机管理”里添加打印机。如果提示需要一台电脑帮忙，页面会直接给出安装命令。</li>
          <li>2. 找一台已经能正常打印它的 Windows 电脑，用管理员身份打开 PowerShell 7。</li>
          <li>3. 粘贴并运行命令一次。之后这台电脑保持开机，转发程序会自动重连。</li>
        </ol>
      </SectionCard>

      <Modal isOpen={open} onOpenChange={(v) => !v && (setOpen(false), setToken(""), setCreated(null))}>
        <Modal.Backdrop>
          <Modal.Container>
            <Modal.Dialog className="sm:max-w-[620px]">
              <Modal.CloseTrigger />
              <Modal.Header>
                <Modal.Heading>设置一台帮忙转发的电脑</Modal.Heading>
              </Modal.Header>
              <Modal.Body className="space-y-3">
                <Field label="这台电脑的名字" hint="建议写位置，例如：三楼打印电脑">
                  <Input fullWidth value={name} onChange={(e) => setName(e.target.value)} />
                </Field>
                <Field label="负责哪台打印机" hint="只有需要 Windows 电脑帮忙转发的打印机才会出现在这里">
                  <Select fullWidth selectedKey={printerID} onSelectionChange={(k) => setPrinterID(String(k))}>
                    <Select.Trigger>
                      <Select.Value />
                      <Select.Indicator />
                    </Select.Trigger>
                    <Select.Popover>
                      <ListBox>
                        {boundPrinters.map((p) => (
                          <ListBox.Item id={p.id} key={p.id} textValue={p.display_name}>
                            {p.display_name}
                            <ListBox.ItemIndicator />
                          </ListBox.Item>
                        ))}
                      </ListBox>
                    </Select.Popover>
                  </Select>
                </Field>
                <Field label="Windows 里的打印机名称（可选）" hint="留空则打到那台电脑的默认打印机">
                  <Input fullWidth placeholder="例如：Pantum MS6000NW Series" value={queue} onChange={(e) => setQueue(e.target.value)} />
                </Field>
                {token ? (
                  <div className="space-y-2">
                    <div className="rounded-xl border border-warning/40 bg-warning/10 p-3 text-xs text-warning">
                      密钥只显示这一次。把下面整段命令复制到那台 Windows 电脑的 PowerShell 7 运行。
                    </div>
                    <pre className="scroll-thin max-h-56 overflow-auto rounded-xl bg-default/60 p-3 text-[11px] leading-5">
                      {created?.script || script(token, created?.id || "")}
                    </pre>
                    <Button
                      size="sm"
                      variant="secondary"
                      onPress={() => {
                        void navigator.clipboard.writeText(created?.script || script(token, created?.id || ""));
                        notifySuccess("已复制");
                      }}
                    >
                      复制命令
                    </Button>
                  </div>
                ) : null}
              </Modal.Body>
              <Modal.Footer>
                <Button slot="close" variant="secondary">
                  关闭
                </Button>
                {!token ? (
                  <Button isDisabled={busy || !name.trim() || !printerID} onPress={create}>
                    {busy ? "创建中…" : "创建"}
                  </Button>
                ) : null}
              </Modal.Footer>
            </Modal.Dialog>
          </Modal.Container>
        </Modal.Backdrop>
      </Modal>
    </div>
  );
}

// Old records were named "助手-4F-wowC" when they were created. Strip the
// machine prefix/suffix so the list reads like a person named the computer.
function prettyAgentName(name: string) {
  return name
    .replace(/^转发：/, "")
    .replace(/^助手-/, "")
    .replace(/-[A-Za-z0-9_-]{3,}$/, "");
}

function CheckGlyph() {
  return (
    <svg fill="none" height="11" viewBox="0 0 24 24" width="11">
      <path d="m5 12.5 4.2 4.2L19 7" stroke="currentColor" strokeLinecap="round" strokeLinejoin="round" strokeWidth="3" />
    </svg>
  );
}
