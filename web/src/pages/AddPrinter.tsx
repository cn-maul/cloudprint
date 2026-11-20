import {useCallback, useEffect, useMemo, useRef, useState, type ReactNode} from "react";
import {Button, Chip, Input, Spinner} from "@heroui/react";
import {
  api,
  type Candidate,
  type CUPSDriver,
  type DiscoveryRun,
  type Printer,
  type QuickAddResult,
  type ScanTargets,
  type SupportedModel,
} from "../lib/api";
import {notifyError, notifySuccess} from "../lib/toast";
import {
  ArrowLeftIcon,
  CheckIcon,
  ChevronDownIcon,
  NetworkIcon,
  PlusCircleIcon,
  PlusIcon,
  PrinterIcon,
  RefreshIcon,
  SearchIcon,
} from "../components/Icon";

type Mode = "scan" | "manual";

export default function AddPrinterPage({
  onBack,
  onAdded,
}: {
  onBack: () => void;
  onAdded: (id: string) => void;
}) {
  const [mode, setMode] = useState<Mode>("scan");
  const [targets, setTargets] = useState<ScanTargets | null>(null);
  const [cidr, setCidr] = useState("");
  const [scanning, setScanning] = useState(false);
  const [run, setRun] = useState<DiscoveryRun | null>(null);
  const [candidates, setCandidates] = useState<Candidate[]>([]);
  const [adding, setAdding] = useState("");
  const [name, setName] = useState("");
  const [address, setAddress] = useState("");
  const [drivers, setDrivers] = useState<CUPSDriver[]>([]);
  const [ppd, setPpd] = useState("");
  const [raw, setRaw] = useState(false);
  const [models, setModels] = useState<SupportedModel[]>([]);
  const [model, setModel] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const timer = useRef<number | null>(null);

  const loadCandidates = useCallback(async () => {
    try {
      const r = await api<{candidates: Candidate[]}>("/api/admin/candidates");
      const list = (r.candidates ?? []).filter((c) => c.status === "new");
      list.sort((a, b) => a.host.localeCompare(b.host, undefined, {numeric: true}));
      setCandidates(list);
    } catch (e) {
      notifyError("读取扫描结果失败", e instanceof Error ? e.message : undefined);
    }
  }, []);

  useEffect(() => {
    api<ScanTargets>("/api/admin/scan-targets")
      .then((r) => {
        setTargets(r);
        setCidr((cur) => cur || r.targets?.[0]?.cidr || "");
      })
      .catch(() => setTargets(null));
    api<{drivers: CUPSDriver[]}>("/api/admin/cups/drivers")
      .then((r) => setDrivers(r.drivers ?? []))
      .catch(() => setDrivers([]));
    api<{models: SupportedModel[]}>("/api/admin/supported-models")
      .then((r) => setModels(r.models ?? []))
      .catch(() => setModels([]));
    void loadCandidates();
    return () => {
      if (timer.current) window.clearTimeout(timer.current);
    };
  }, [loadCandidates]);

  const poll = useCallback(
    async (id: string) => {
      try {
        const r = await api<{runs: DiscoveryRun[]}>("/api/admin/discovery-runs");
        const current = r.runs?.find((x) => x.id === id);
        if (!current) return;
        setRun(current);
        if (current.status === "running") {
          timer.current = window.setTimeout(() => void poll(id), 1200);
          return;
        }
        setScanning(false);
        await loadCandidates();
        if (current.error_message) notifyError("扫描未完成", current.error_message);
      } catch {
        timer.current = window.setTimeout(() => void poll(id), 2000);
      }
    },
    [loadCandidates],
  );

  async function scan() {
    const value = cidr.trim();
    if (!value) return;
    setScanning(true);
    setRun(null);
    try {
      const r = await api<DiscoveryRun>("/api/admin/discover", {
        method: "POST",
        body: JSON.stringify({mode: "scan", cidr: value}),
      });
      setRun(r);
      void poll(r.id);
    } catch (e) {
      setScanning(false);
      notifyError("无法开始扫描", e instanceof Error ? e.message : undefined);
    }
  }

  async function addCandidate(c: Candidate) {
    if (c.kind === "socket") {
      // RAW devices cannot report a model, so there is nothing to match a
      // driver against. Send the admin to the manual form with the address
      // already filled in.
      setName(c.model || c.vendor || c.host);
      setAddress(`socket://${c.host}:${c.port || 9100}`);
      setPpd("");
      setRaw(true);
      // A RAW device cannot say what it is, so the model has to be picked on
      // purpose. Pre-selecting the only entry would label an unsupported
      // printer as a Pantum and send it the wrong raster dialect.
      setModel("");
      setMode("manual");
      return;
    }
    setAdding(c.id);
    try {
      const r = await api<QuickAddResult>("/api/admin/printers/quick-add", {
        method: "POST",
        body: JSON.stringify({
          display_name: c.model || c.vendor || c.host,
          uri: c.discovered_uri,
          vendor: c.vendor || "",
          model: c.model || "",
          candidate_id: c.id,
        }),
      });
      notifySuccess("已添加", r.message);
      onAdded(r.printer.id);
      void loadCandidates();
    } catch (e) {
      const code = (e as Error & {code?: string}).code;
      if (code === "DRIVER_REQUIRED") {
        setName(c.model || c.vendor || c.host);
        setAddress(c.discovered_uri);
        setPpd("");
        setMode("manual");
        notifyError("需要选择驱动", e instanceof Error ? e.message : undefined);
      } else {
        notifyError("添加失败", e instanceof Error ? e.message : undefined);
      }
    } finally {
      setAdding("");
    }
  }

  async function addManual() {
    const displayName = name.trim();
    const uri = address.trim();
    if (!displayName || !uri) return;
    setSubmitting(true);
    try {
      const r = await api<QuickAddResult>("/api/admin/printers/quick-add", {
        method: "POST",
        body: JSON.stringify({
          display_name: displayName,
          uri,
          ppd,
            model: models.find((m) => m.id === model)?.name || "",
          driver_mode: raw ? "socket" : "",
        }),
      });
      notifySuccess("已添加", r.message);
      onAdded(r.printer.id);
    } catch (e) {
      notifyError("添加失败", e instanceof Error ? e.message : undefined);
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <div className="rise space-y-5">
      <div className="flex items-center gap-2">
        <Button isIconOnly aria-label="返回" size="sm" variant="ghost" onPress={onBack}>
          <ArrowLeftIcon size={18} />
        </Button>
        <h1 className="text-xl font-semibold tracking-tight">添加打印机</h1>
      </div>

      <div className="grid grid-cols-2 gap-1 rounded-xl border border-separator/60 bg-surface/50 p-1">
        <ModeButton active={mode === "scan"} label="扫描网段" onPress={() => setMode("scan")}>
          <NetworkIcon size={16} />
        </ModeButton>
        <ModeButton
          active={mode === "manual"}
          label="手动添加"
          onPress={() => {
            setMode("manual");
            // Opening the form by hand is an explicit choice, so preselect the
            // first supported model; the RAW path leaves it empty on purpose.
            setModel((cur) => cur || models[0]?.id || "");
          }}
        >
          <PlusCircleIcon size={16} />
        </ModeButton>
      </div>

      {mode === "scan" ? (
        <section className="space-y-4 rounded-2xl border border-separator/60 bg-surface/60 p-5">
          <div className="flex flex-col gap-2 sm:flex-row">
            <Input
              className="mono"
              fullWidth
              placeholder="192.168.2.0/24"
              value={cidr}
              onChange={(e) => setCidr(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === "Enter" && !scanning) void scan();
              }}
            />
            <Button isDisabled={scanning || !cidr.trim() || targets?.can_scan === false} onPress={() => void scan()}>
              {scanning ? <Spinner size="sm" /> : <SearchIcon size={16} />}
              {scanning ? "扫描中" : "扫描"}
            </Button>
          </div>

          {targets?.targets?.length ? (
            <div className="flex flex-wrap items-center gap-1.5">
              {targets.targets.map((t) => (
                <button
                  className={`mono rounded-md border px-2 py-1 text-[11px] transition ${
                    t.cidr === cidr
                      ? "border-accent/60 bg-accent/10 text-accent"
                      : "border-separator/70 text-muted hover:border-accent/40 hover:text-foreground"
                  }`}
                  key={t.cidr}
                  onClick={() => setCidr(t.cidr)}
                  type="button"
                >
                  {t.cidr}
                </button>
              ))}
            </div>
          ) : null}

          <div className="flex items-center justify-between border-t border-separator/50 pt-3">
            <span className="mono text-[11px] text-muted">
              {run?.status === "running"
                ? "扫描中"
                : run
                  ? `${run.found_count} 台`
                  : `${candidates.length} 台`}
            </span>
            <button
              aria-label="刷新结果"
              className="grid size-7 place-items-center rounded-full text-muted transition hover:bg-default/60 hover:text-foreground"
              onClick={() => void loadCandidates()}
              type="button"
            >
              <RefreshIcon size={14} />
            </button>
          </div>

          {candidates.length === 0 ? (
            <div className="grid place-items-center gap-2 py-10 text-muted/60">
              <NetworkIcon size={26} />
            </div>
          ) : (
            <ul className="divide-y divide-separator/40 overflow-hidden rounded-xl border border-separator/50">
              {candidates.map((c) => (
                <li className="flex items-center gap-3 px-4 py-3" key={c.id}>
                  <span className="grid size-9 shrink-0 place-items-center rounded-lg bg-accent/10 text-accent">
                    <PrinterIcon size={17} />
                  </span>
                  <div className="min-w-0 flex-1">
                    <div className="truncate text-sm">{c.model || c.vendor || c.host}</div>
                    <div className="mono mt-0.5 truncate text-[11px] text-muted">
                      {c.host}
                      {c.vendor ? ` · ${c.vendor}` : ""}
                    </div>
                  </div>
                  {c.already_added ? (
                    <Chip color="default" size="sm" variant="soft">
                      已添加
                    </Chip>
                  ) : c.supported === false && c.kind !== "socket" ? (
                    <Chip
                      color="default"
                      size="sm"
                      title={c.unsupported_reason}
                      variant="soft"
                    >
                      暂不支持
                    </Chip>
                  ) : (
                    <>
                      <Chip color={c.direct ? "success" : "warning"} size="sm" variant="soft">
                        {c.kind === "socket" ? "RAW" : c.direct ? "直连" : "需驱动"}
                      </Chip>
                      <Button
                        isDisabled={adding === c.id}
                        size="sm"
                        variant="secondary"
                        onPress={() => void addCandidate(c)}
                      >
                        {adding === c.id ? <Spinner size="sm" /> : <PlusIcon size={14} />}
                        添加
                      </Button>
                    </>
                  )}
                </li>
              ))}
            </ul>
          )}
        </section>
      ) : (
        <section className="space-y-4 rounded-2xl border border-separator/60 bg-surface/60 p-5">
          <label className="flex flex-col gap-1.5">
            <span className="text-xs text-muted">名称</span>
            <Input
              fullWidth
              placeholder="奔图 MS6000NW"
              value={name}
              onChange={(e) => setName(e.target.value)}
            />
          </label>
          <label className="flex flex-col gap-1.5">
            <span className="text-xs text-muted">地址</span>
            <Input
              className="mono"
              fullWidth
              placeholder="192.168.2.100"
              value={address}
              onChange={(e) => setAddress(e.target.value)}
            />
          </label>
          <div className="flex flex-col gap-1.5">
            <span className="text-xs text-muted">型号</span>
            <div className="flex flex-wrap gap-1.5">
              {models.map((m) => (
                <button
                  className={`rounded-lg border px-3 py-1.5 text-sm transition ${
                    m.id === model
                      ? "border-accent/60 bg-accent/10 text-accent"
                      : "border-separator/70 text-muted hover:border-accent/40 hover:text-foreground"
                  }`}
                  key={m.id}
                  onClick={() => setModel(m.id)}
                  title={m.notes}
                  type="button"
                >
                  {m.name}
                </button>
              ))}
            </div>
          </div>
          <div className="flex flex-col gap-1.5">
            <span className="text-xs text-muted">驱动</span>
            <DriverPicker drivers={drivers} value={ppd} onChange={setPpd} />
          </div>
          <label className="flex cursor-pointer items-center gap-2 text-xs text-muted">
            <input
              checked={raw}
              className="size-3.5 accent-[var(--accent)]"
              onChange={(e) => setRaw(e.target.checked)}
              type="checkbox"
            />
            RAW 9100
          </label>
          <Button
            fullWidth
            isDisabled={submitting || !name.trim() || !address.trim() || !model}
            onPress={() => void addManual()}
          >
            {submitting ? <Spinner size="sm" /> : <PlusIcon size={16} />}
            添加
          </Button>
        </section>
      )}
    </div>
  );
}

function ModeButton({
  active,
  label,
  onPress,
  children,
}: {
  active: boolean;
  label: string;
  onPress: () => void;
  children: ReactNode;
}) {
  return (
    <button
      className={`flex items-center justify-center gap-1.5 rounded-lg px-3 py-2 text-sm transition ${
        active ? "bg-surface font-medium text-foreground shadow-sm" : "text-muted hover:text-foreground"
      }`}
      onClick={onPress}
      type="button"
    >
      {children}
      {label}
    </button>
  );
}

function DriverPicker({
  drivers,
  value,
  onChange,
}: {
  drivers: CUPSDriver[];
  value: string;
  onChange: (path: string) => void;
}) {
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState("");
  const selected = drivers.find((d) => d.path === value);

  const filtered = useMemo(() => {
    const q = query.trim().toLowerCase();
    if (!q) return drivers.slice(0, 80);
    // The CUPS driver list has thousands of models; matching on the full list
    // is cheap, but rendering is not, so only the best 80 are shown.
    const words = q.split(/\s+/).filter(Boolean);
    const scored: Array<{d: CUPSDriver; score: number}> = [];
    for (const d of drivers) {
      const hay = `${d.name} ${d.manufacturer ?? ""} ${d.model ?? ""}`.toLowerCase();
      let score = 0;
      let all = true;
      for (const w of words) {
        const at = hay.indexOf(w);
        if (at < 0) {
          all = false;
          break;
        }
        score += at === 0 ? 3 : 1;
      }
      if (all) scored.push({d, score});
    }
    scored.sort((a, b) => b.score - a.score || a.d.name.localeCompare(b.d.name));
    return scored.slice(0, 80).map((s) => s.d);
  }, [drivers, query]);

  return (
    <div className="relative">
      <button
        className="flex w-full items-center gap-2 rounded-lg border border-separator/70 bg-surface px-3 py-2 text-left text-sm transition hover:border-accent/40"
        onClick={() => setOpen((v) => !v)}
        type="button"
      >
        <span className="min-w-0 flex-1 truncate">
          {selected ? selected.name : "自动匹配驱动"}
        </span>
        <span className={`text-muted transition ${open ? "rotate-180" : ""}`}>
          <ChevronDownIcon size={14} />
        </span>
      </button>

      {open ? (
        <>
          <div className="fixed inset-0 z-20" onClick={() => setOpen(false)} />
          <div className="absolute z-30 mt-1 w-full overflow-hidden rounded-xl border border-separator/70 bg-surface shadow-xl">
            <div className="border-b border-separator/50 p-2">
              <Input
                autoFocus
                fullWidth
                placeholder="搜索驱动"
                value={query}
                onChange={(e) => setQuery(e.target.value)}
              />
            </div>
            <ul className="max-h-64 overflow-auto scroll-thin">
              <li>
                <button
                  className="flex w-full items-center gap-2 px-3 py-2 text-left text-sm transition hover:bg-default/50"
                  onClick={() => {
                    onChange("");
                    setOpen(false);
                  }}
                  type="button"
                >
                  <span className="flex-1">自动匹配驱动</span>
                  {!value ? <CheckIcon size={14} /> : null}
                </button>
              </li>
              {filtered.length === 0 ? (
                <li className="px-3 py-6 text-center text-xs text-muted">没有匹配的驱动</li>
              ) : null}
              {filtered.map((d) => (
                <li key={d.path}>
                  <button
                    className="flex w-full items-center gap-2 px-3 py-2 text-left text-sm transition hover:bg-default/50"
                    onClick={() => {
                      onChange(d.path);
                      setOpen(false);
                    }}
                    type="button"
                  >
                    <span className="min-w-0 flex-1 truncate">{d.name}</span>
                    {d.path === value ? <CheckIcon size={14} /> : null}
                  </button>
                </li>
              ))}
            </ul>
          </div>
        </>
      ) : null}
    </div>
  );
}
