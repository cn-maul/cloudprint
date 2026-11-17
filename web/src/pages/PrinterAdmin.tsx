import {useCallback, useEffect, useMemo, useState} from "react";
import {Button, Chip, Input, Modal, Switch} from "@heroui/react";
import {
  api,
  fmt,
  healthLabel,
  type Candidate,
  type DiscoveryRun,
  type Printer,
  type QuickAddResult,
} from "../lib/api";
import {notifyError, notifySuccess, notifyWarning} from "../lib/toast";
import {EmptyBox, Field, PageHeader, SectionCard} from "../components/ui";

type Setup = NonNullable<QuickAddResult["agent"]>;

export default function PrinterAdminPage() {
  const [printers, setPrinters] = useState<Printer[]>([]);
  const [candidates, setCandidates] = useState<Candidate[]>([]);
  const [runs, setRuns] = useState<DiscoveryRun[]>([]);
  const [loading, setLoading] = useState(true);
  const [scanning, setScanning] = useState(false);
  const [scanNote, setScanNote] = useState("");
  const [addOpen, setAddOpen] = useState(false);
  const [addSeed, setAddSeed] = useState<Candidate | null>(null);
  const [reconnect, setReconnect] = useState<Printer | null>(null);
  const [setup, setSetup] = useState<Setup | null>(null);
  const [busyId, setBusyId] = useState("");
  const [toggling, setToggling] = useState("");

  const load = useCallback(async () => {
    try {
      const [p, c, r] = await Promise.all([
        api<{printers: Printer[]}>("/api/admin/printers"),
        api<{candidates: Candidate[]}>("/api/admin/candidates"),
        api<{runs: DiscoveryRun[]}>("/api/admin/discovery-runs"),
      ]);
      setPrinters(p.printers.map((x) => ({...x, endpoints: x.endpoints ?? []})));
      setCandidates(c.candidates);
      setRuns(r.runs);
    } catch (e) {
      notifyError("载入打印机失败", e instanceof Error ? e.message : "");
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  const discovered = useMemo(() => {
    const known = new Set<string>();
    const knownHosts = new Set<string>();
    for (const p of printers) {
      if (p.network_address) knownHosts.add(p.network_address.toLowerCase());
      for (const ep of p.endpoints ?? []) {
        known.add(ep.uri.toLowerCase());
        const host = hostOf(ep.uri);
        if (host) knownHosts.add(host);
      }
    }
    const seen = new Set<string>();
    const out: Candidate[] = [];
    for (const c of candidates) {
      if (c.status !== "new" || c.already_added) continue;
      if (!c.direct && !c.probe_attempted) continue;
      if (known.has(c.discovered_uri.toLowerCase())) continue;
      if (c.host && knownHosts.has(c.host.toLowerCase())) continue;
      const key = `${c.host}|${c.model ?? ""}`;
      if (seen.has(key)) continue;
      seen.add(key);
      out.push(c);
    }
    return out;
  }, [candidates, printers]);

  async function scan() {
    setScanning(true);
    setScanNote("正在搜索局域网里的打印机…");
    try {
      const run = await api<DiscoveryRun>("/api/admin/discover", {
        method: "POST",
        body: JSON.stringify({mode: "all"}),
      });
      const deadline = Date.now() + 70000;
      let finished = false;
      while (Date.now() < deadline) {
        await new Promise((r) => setTimeout(r, 2500));
        const res = await api<{runs: DiscoveryRun[]}>("/api/admin/discovery-runs");
        setRuns(res.runs);
        const current = res.runs.find((x) => x.id === run.id);
        if (current && current.status !== "running") {
          finished = true;
          await load();
          notifySuccess(
            current.found_count > 0 ? "搜索完成" : "搜索完成",
            current.found_count > 0 ? "在下面确认名称，点“添加”即可" : "没有发现新打印机，可以在页面顶部手动输入 IP",
          );
          break;
        }
      }
      if (!finished) {
        await load();
        setScanNote("扫描仍在后台进行，稍后点“刷新结果”。");
      } else {
        setScanNote("");
      }
    } catch (e) {
      setScanNote("");
      notifyError("扫描失败", e instanceof Error ? e.message : "");
    } finally {
      setScanning(false);
    }
  }

  async function check(p: Printer) {
    const enabled = (p.endpoints ?? []).filter((ep) => ep.is_enabled);
    if (enabled.length === 0) {
      setReconnect(p);
      return;
    }
    setBusyId(p.id);
    try {
      await api(`/api/admin/printers/${p.id}/probe`, {method: "POST"});
      await load();
      notifySuccess("检测完成");
    } catch (e) {
      notifyError("检测失败", e instanceof Error ? e.message : "");
      await load();
    } finally {
      setBusyId("");
    }
  }

  async function toggle(p: Printer) {
    setToggling(p.id);
    try {
      await api(`/api/admin/printers/${p.id}`, {
        method: "PATCH",
        body: JSON.stringify({is_enabled: !p.is_enabled}),
      });
      await load();
    } catch (e) {
      notifyError("修改失败", e instanceof Error ? e.message : "");
    } finally {
      setToggling("");
    }
  }

  async function remove(p: Printer) {
    if (!window.confirm(`确定删除“${p.display_name}”？已提交的打印任务记录会保留。`)) return;
    try {
      await api(`/api/admin/printers/${p.id}`, {method: "DELETE"});
      notifySuccess("已删除", p.display_name);
      void load();
    } catch (e) {
      notifyError("删除失败", e instanceof Error ? e.message : "");
    }
  }

  async function ignore(c: Candidate) {
    try {
      await api(`/api/admin/candidates/${c.id}/ignore`, {method: "POST"});
      void load();
    } catch (e) {
      notifyError("操作失败", e instanceof Error ? e.message : "");
    }
  }

  const lastRun = runs[0];
  const ungrouped = printers.filter((p) => p.is_enabled && (p.group_ids ?? []).length === 0);

  return (
    <div className="space-y-5">
      <PageHeader
        actions={
          <>
            <Button size="sm" variant="secondary" onPress={() => void load()}>
              刷新
            </Button>
            <Button
              size="sm"
              onPress={() => void scan()}
              isDisabled={scanning}
              className={scanning ? "" : "shadow-sm ring-1 ring-accent/20"}
            >
              {scanning ? "正在搜索…" : "搜索打印机"}
            </Button>
            <Button
              size="sm"
              variant="secondary"
              onPress={() => {
                setAddSeed(null);
                setAddOpen(true);
              }}
            >
              手动添加
            </Button>
          </>
        }
        subtitle="点“搜索打印机”，找到后确认名字再添加。找不到也可以手动输入地址。"
        title="打印机"
      />

      {printers.length === 0 && !loading ? (
        <div className="grid gap-3 rounded-2xl border border-accent/25 bg-accent/5 p-4 sm:grid-cols-3">
          <Step n="1" title="搜索打印机" text="点右上角“搜索打印机”，等十几秒。" />
          <Step n="2" title="确认并添加" text="在结果里核对名字，点“添加”。" />
          <Step n="3" title="分给同事" text="到“成员与权限”建组，勾选这台打印机。" />
        </div>
      ) : null}

      {ungrouped.length > 0 ? (
        <div className="flex flex-col gap-2 rounded-2xl border border-warning/30 bg-warning/8 p-4 sm:flex-row sm:items-center sm:justify-between">
          <div className="text-sm leading-6 text-warning">
            <span className="font-medium">
              {ungrouped.map((p) => p.display_name).join("、")}
            </span>{" "}
            还没分配给任何分组，普通同事现在看不到它。
          </div>
          <Button size="sm" variant="secondary" onPress={() => (window.location.hash = "/access")}>
            去分组
          </Button>
        </div>
      ) : null}

      <SectionCard
        description={
          scanning
            ? "正在搜索公司网络里的打印机，通常需要 10–40 秒。"
            : discovered.length > 0
              ? `找到 ${discovered.length} 台还没添加的打印机。已添加过的不会重复显示。`
              : lastRun
                ? `上次搜索：${fmt.date(lastRun.started_at)} · 没有待添加的打印机`
                : "会自动搜索这台服务器能连接到的公司网络。"
        }
        title={scanning || scanNote ? scanNote || "搜索中…" : "搜索结果"}
      >
        {discovered.length === 0 ? (
          <EmptyBox
            action={
              <div className="flex flex-wrap justify-center gap-2">
                <Button size="sm" onPress={() => void scan()} isDisabled={scanning}>
                  {scanning ? "正在搜索…" : "搜索打印机"}
                </Button>
                <span className="text-xs text-muted">找不到时，可以在页面顶部“手动添加”里填打印机地址。</span>
              </div>
            }
            description="这里只显示还没添加过的打印机。请确认打印机已开机并连着公司网络；也可以手动填写地址。"
            title={scanning ? "正在搜索…" : "没有待添加的打印机"}
          />
        ) : (
          <div className="space-y-2">
            {discovered.map((c) => (
              <div
                className="flex flex-col gap-3 rounded-xl border border-separator/60 bg-surface/50 p-4 sm:flex-row sm:items-center"
                key={c.id}
              >
                <div className="min-w-0 flex-1">
                  <div className="flex flex-wrap items-center gap-2">
                    <span className="font-medium">{c.model || "网络打印机"}</span>
                    <Chip color={c.direct ? "success" : c.probe_attempted ? "warning" : "default"} size="sm" variant="soft">
                      {c.direct ? "可以直接打印" : c.probe_attempted ? "需要一台电脑帮忙转发" : "暂时没响应"}
                    </Chip>
                    {c.vendor ? (
                      <span className="text-xs text-muted">{c.vendor}</span>
                    ) : null}
                  </div>
                  <div className="mt-1 text-xs text-muted">
                    地址 <span className="mono">{c.host}</span>
                    {c.direct
                      ? " · 添加后同事就能用"
                      : c.probe_attempted
                        ? " · 添加后需要在一台能打印它的 Windows 电脑上安装一次转发程序"
                        : " · 先确认打印机开机、连着公司网络"}
                  </div>
                </div>
                <div className="flex shrink-0 gap-2">
                  <Button
                    size="sm"
                    onPress={() => {
                      setAddSeed(c);
                      setAddOpen(true);
                    }}
                  >
                    添加
                  </Button>
                  <Button size="sm" variant="ghost" onPress={() => void ignore(c)}>
                    忽略
                  </Button>
                </div>
              </div>
            ))}
          </div>
        )}
      </SectionCard>

      <SectionCard
        actions={
          <span className="text-xs text-muted">
            共 {printers.length} 台 · {printers.filter((p) => p.is_enabled).length} 台已开放
          </span>
        }
        description="同事只能看到“已开放”的打印机。打印机不在线时，同事会看到灰掉的入口和原因。"
        title="已添加的打印机"
      >
        {printers.length === 0 && !loading ? (
          <EmptyBox
            action={
              <Button size="sm" onPress={() => void scan()} isDisabled={scanning}>
                搜索打印机
              </Button>
            }
            description="先搜索，或手动填写打印机 IP。"
            title="还没有添加打印机"
          />
        ) : (
          <div className="space-y-3">
            {printers.map((p) => (
              <PrinterCard
                busy={busyId === p.id}
                toggling={toggling === p.id}
                key={p.id}
                onCheck={() => void check(p)}
                onRemove={() => void remove(p)}
                onToggle={() => void toggle(p)}
                printer={p}
              />
            ))}
          </div>
        )}
      </SectionCard>

      <AddPrinterModal
        seed={addSeed}
        open={addOpen}
        printer={null}
        onClose={() => setAddOpen(false)}
        onDone={async (result, setupInfo) => {
          setAddOpen(false);
          await load();
          if (setupInfo) setSetup(setupInfo);
          else notifySuccess("打印机已添加", result.message);
        }}
      />

      <AddPrinterModal
        seed={null}
        open={!!reconnect}
        printer={reconnect}
        onClose={() => setReconnect(null)}
        onDone={async (result, setupInfo) => {
          setReconnect(null);
          await load();
          if (setupInfo) setSetup(setupInfo);
          else notifySuccess("已接入", result.message);
        }}
      />

      <Modal isOpen={!!setup} onOpenChange={(v) => !v && setSetup(null)}>
        <Modal.Backdrop>
          <Modal.Container size="lg">
            <Modal.Dialog className="sm:max-w-[720px]">
              <Modal.CloseTrigger />
              <Modal.Header>
              <Modal.Heading>最后一步：在那台 Windows 电脑上运行一次</Modal.Heading>
              </Modal.Header>
              <Modal.Body className="space-y-3">
                <p className="text-sm leading-6 text-muted">
                  打印机已经记录好了，但它需要一台 Windows 电脑帮忙转发。
                  请找一台<strong>已经装好驱动、现在就能正常打印它</strong>的电脑，
                  用管理员身份打开 PowerShell 7，把下面整段命令粘贴进去运行一次。
                  之后这台电脑保持开机即可。
                </p>
                <div className="relative">
                  <pre className="scroll-thin max-h-72 overflow-auto rounded-xl bg-default/60 p-4 text-[11px] leading-5">
                    {setup?.setup_script}
                  </pre>
                  <Button
                    className="absolute right-2 top-2"
                    size="sm"
                    variant="secondary"
                    onPress={() => {
                      if (setup) {
                        void navigator.clipboard.writeText(setup.setup_script);
                        notifySuccess("已复制");
                      }
                    }}
                  >
                    复制命令
                  </Button>
                </div>
                <p className="rounded-xl bg-warning/10 px-3 py-2 text-xs leading-5 text-warning">
                  这段命令只显示这一次，只发给负责这台电脑的同事。
                  如果之后仍不出纸，先在 Windows 的“打印机和扫描仪”里确认这台打印机在列表中，并手动打印一次。
                </p>
              </Modal.Body>
              <Modal.Footer>
                <Button variant="secondary" onPress={() => setSetup(null)}>
                  稍后再弄
                </Button>
              </Modal.Footer>
            </Modal.Dialog>
          </Modal.Container>
        </Modal.Backdrop>
      </Modal>
    </div>
  );
}

function hostOf(uri: string) {
  const m = uri.match(/^(?:ipps?|socket):\/\/([^/:]+)/i);
  return m ? m[1].toLowerCase() : "";
}

// Older records are named "助手-4F-wowC"; show the part a person recognizes.
function prettyAgentName(name: string) {
  return name
    .replace(/^转发：/, "")
    .replace(/^助手-/, "")
    .replace(/-[A-Za-z0-9_-]{3,}$/, "");
}

function Step({n, title, text}: {n: string; title: string; text: string}) {
  return (
    <div className="flex gap-3">
      <span className="grid size-7 shrink-0 place-items-center rounded-full bg-accent text-xs font-semibold text-accent-foreground">
        {n}
      </span>
      <div className="min-w-0">
        <div className="text-sm font-medium">{title}</div>
        <div className="mt-0.5 text-xs leading-5 text-muted">{text}</div>
      </div>
    </div>
  );
}

function PrinterCard({
  printer,
  busy,
  toggling,
  onCheck,
  onToggle,
  onRemove,
}: {
  printer: Printer;
  busy: boolean;
  toggling: boolean;
  onCheck: () => void;
  onToggle: () => void;
  onRemove: () => void;
}) {
  // Disabled rows are history from a previous connection. Counting them makes
  // the card report an old, unreachable path after "重新接入".
  const endpoints = (printer.endpoints ?? []).filter((ep) => ep.is_enabled);
  const direct = endpoints.find((ep) => ep.kind === "ipp");
  const agent = endpoints.find((ep) => ep.kind === "agent");
  const best = direct ?? agent;
  const health = best ? healthLabel[best.health_status] ?? healthLabel.unknown : healthLabel.unknown;
  const state = !best
    ? "还没连接"
    : best.kind === "agent"
      ? best.health_status === "healthy"
        ? "可以打印"
        : best.health_status === "down"
          ? "帮忙转发的电脑没开机"
          : "待检测"
      : best.health_status === "healthy"
        ? "可以打印"
        : best.health_status === "down"
          ? "暂时连不上"
          : best.health_status === "degraded"
            ? "还需要一台电脑帮忙转发"
            : "待检测";
  const where =
    printer.network_address ||
    (direct ? direct.uri.replace(/^ipps?:\/\//, "").replace(/\/.*$/, "") : "");

  return (
    <div className="rounded-2xl border border-separator/60 bg-surface/50 p-4">
      <div className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
        <div className="min-w-0">
          <div className="flex flex-wrap items-center gap-2">
            <span className="truncate font-medium">{printer.display_name}</span>
            <Chip color={health.color} size="sm" variant="soft">
              {state}
            </Chip>
            {!printer.is_enabled ? (
              <Chip color="default" size="sm" variant="soft">
                未开放给同事
              </Chip>
            ) : null}
          </div>
          <div className="mt-1 text-xs text-muted">
            {[printer.location, printer.vendor, printer.model].filter(Boolean).join(" · ") || "未填写位置和型号"}
            {where ? <span className="mono"> · {where}</span> : null}
          </div>
          {best?.last_error ? (
            <div className="mt-1 text-xs text-danger">
              {best.kind === "agent" && best.health_status === "down"
                ? "负责转发的 Windows 电脑现在不在线。请让那台电脑开机并保持联网；如果换过电脑，重新生成一次安装命令。"
                : best.last_error}
            </div>
          ) : null}
          {agent && agent.health_status !== "healthy" ? (
            <div className="mt-1 text-xs text-muted">
              负责转发的电脑：{" "}
              <span className="font-medium text-foreground">
                {agent.agent_name ? prettyAgentName(agent.agent_name) : "未命名电脑"}
              </span>
            </div>
          ) : null}
          {best?.last_checked_at ? (
            <div className="mt-1 text-[11px] text-muted">上次检测 {fmt.date(best.last_checked_at)}</div>
          ) : null}
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <label className="flex items-center gap-2 text-xs text-muted">
            <Switch isSelected={printer.is_enabled} size="sm" isDisabled={toggling} onChange={onToggle}>
              <Switch.Content>
                <Switch.Control>
                  <Switch.Thumb />
                </Switch.Control>
              </Switch.Content>
            </Switch>
            开放给同事
          </label>
          <Button size="sm" variant="secondary" isDisabled={busy} onPress={onCheck}>
            {busy ? "检测中…" : endpoints.length === 0 ? "重新接入" : "检测一下"}
          </Button>
          <Button size="sm" variant="danger-soft" onPress={onRemove}>
            删除
          </Button>
        </div>
      </div>
    </div>
  );
}

function AddPrinterModal({
  open,
  seed,
  printer,
  onClose,
  onDone,
}: {
  open: boolean;
  seed: Candidate | null;
  printer: Printer | null;
  onClose: () => void;
  onDone: (result: QuickAddResult, setup?: Setup) => Promise<void> | void;
}) {
  const [name, setName] = useState("");
  const [location, setLocation] = useState("");
  const [address, setAddress] = useState("");
  const [queue, setQueue] = useState("");
  const [busy, setBusy] = useState(false);
  const [showAdvanced, setShowAdvanced] = useState(false);

  useEffect(() => {
    if (!open) return;
    setName(printer?.display_name || seed?.model?.trim() || "");
    setLocation(printer?.location || "");
    setAddress(seed?.host || "");
    setQueue("");
    setShowAdvanced(false);
  }, [open, seed, printer]);

  function normalizeAddress() {
    let v = address.trim();
    if (!v) return "";
    if (/^ipps?:\/\//i.test(v)) return v;
    if (/^socket:\/\//i.test(v)) return v;
    if (/^\d{1,3}(\.\d{1,3}){3}(:\d+)?$/.test(v)) {
      const [host, port] = v.split(":");
      return `ipp://${host}:${port || "631"}/ipp/print`;
    }
    if (/^[a-z0-9.-]+$/i.test(v)) return `ipp://${v}:631/ipp/print`;
    return v;
  }

  async function submit() {
    if (!name.trim()) {
      notifyWarning("请给打印机起个名字", "例如：三楼办公室打印机");
      return;
    }
    const uri = normalizeAddress();
    if (!uri) {
      notifyWarning("请填写打印机地址", "填打印机屏幕上或机身标签上的 IP，例如 192.168.2.91");
      return;
    }
    setBusy(true);
    try {
      const result = await api<QuickAddResult>("/api/admin/printers/quick-add", {
        method: "POST",
        body: JSON.stringify({
          display_name: name.trim(),
          location: location.trim(),
          uri,
          vendor: seed?.vendor || "",
          model: seed?.model || "",
          mode: seed && !seed.direct ? "agent" : "direct",
          helper_name: queue.trim(),
          candidate_id: seed?.id || "",
          printer_id: printer?.id || "",
        }),
      });
      await onDone(result, result.agent);
    } catch (e) {
      notifyError("添加失败", e instanceof Error ? e.message : "");
    } finally {
      setBusy(false);
    }
  }

  return (
    <Modal isOpen={open} onOpenChange={(v) => !v && onClose()}>
      <Modal.Backdrop>
        <Modal.Container>
          <Modal.Dialog className="sm:max-w-[560px]">
            <Modal.CloseTrigger />
            <Modal.Header>
              <Modal.Heading>{printer ? `为“${printer.display_name}”接入打印机` : "添加打印机"}</Modal.Heading>
            </Modal.Header>
            <Modal.Body className="space-y-4">
              {seed ? (
                <div className="rounded-xl bg-default/50 px-3 py-2 text-xs leading-5 text-muted">
                  已找到 <span className="mono">{seed.host}</span>
                  {seed.model ? ` · ${seed.model}` : ""}。
                  {seed.direct ? "这台设备可以直接接收打印文件。" : "这台设备需要一台 Windows 电脑转发。"}
                </div>
              ) : null}

              <Field label="给同事看的名字" hint="建议带位置，方便辨认">
                <Input
                  fullWidth
                  placeholder="例如：三楼办公室打印机"
                  value={name}
                  onChange={(e) => setName(e.target.value)}
                />
              </Field>
              <Field label="位置（可选）">
                <Input
                  fullWidth
                  placeholder="例如：三楼东侧"
                  value={location}
                  onChange={(e) => setLocation(e.target.value)}
                />
              </Field>

              {!seed ? (
              <Field label="打印机地址" hint="在打印机屏幕、机身标签或路由器里能看到，一般是 192.168.x.x。">
                  <Input
                    className="mono"
                    fullWidth
                    placeholder="192.168.2.91"
                    value={address}
                    onChange={(e) => setAddress(e.target.value)}
                  />
                </Field>
              ) : null}

              <div className="rounded-xl bg-default/50 px-3 py-2.5 text-xs leading-5 text-muted">
                添加后会自动检测这台打印机。能直接连就自动接好；
                不能直接连时，再告诉你去哪台 Windows 电脑上装一次转发程序。
              </div>

              <button
                className="text-xs text-muted underline decoration-dotted underline-offset-4"
                onClick={() => setShowAdvanced((v) => !v)}
                type="button"
              >
                {showAdvanced ? "收起高级选项" : "高级选项"}
              </button>
              {showAdvanced ? (
                <div className="space-y-3 rounded-xl bg-default/40 p-3 text-xs leading-5 text-muted">
                  <Field
                    label="Windows 里的打印机名称（可选）"
                    hint="只有通过 Windows 电脑转发时才用；留空则打到那台电脑的默认打印机"
                  >
                    <Input
                      fullWidth
                      placeholder="例如：Pantum MS6000NW Series"
                      value={queue}
                      onChange={(e) => setQueue(e.target.value)}
                    />
                  </Field>
                  <div>只做只读检测，不会打印测试页，也不会修改打印机设置。</div>
                </div>
              ) : null}
            </Modal.Body>
            <Modal.Footer>
              <Button slot="close" variant="secondary">
                取消
              </Button>
              <Button isDisabled={busy} onPress={submit}>
                {busy ? "添加并检测中…" : "添加打印机"}
              </Button>
            </Modal.Footer>
          </Modal.Dialog>
        </Modal.Container>
      </Modal.Backdrop>
    </Modal>
  );
}
