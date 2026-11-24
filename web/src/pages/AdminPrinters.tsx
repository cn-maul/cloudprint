import {useCallback, useEffect, useState} from "react";
import {Button, Chip, Spinner} from "@heroui/react";
import {
  api,
  endpointKindLabel,
  healthLabel,
  type Printer,
} from "../lib/api";
import {notifyError, notifySuccess} from "../lib/toast";
import {
  ArrowLeftIcon,
  ChevronRightIcon,
  PlusIcon,
  PrinterIcon,
  RefreshIcon,
  TrashIcon,
} from "../components/Icon";

// Printer management for a logged-in admin. Guests never reach this page:
// the shell keeps them on the list, and every request here lives under
// /api/admin, which answers 403 for the guest principal.
export default function AdminPrintersPage({
  onOpen,
  onAdd,
  onBack,
}: {
  onOpen: (id: string) => void;
  onAdd: () => void;
  onBack: () => void;
}) {
  const [printers, setPrinters] = useState<Printer[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState("");

  const load = useCallback(async () => {
    try {
      const r = await api<{printers: Printer[]}>("/api/admin/printers");
      setPrinters((r.printers ?? []).map((p) => ({...p, endpoints: p.endpoints ?? []})));
      setError("");
    } catch (e) {
      setError(e instanceof Error ? e.message : "载入失败");
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  async function toggle(p: Printer) {
    setBusy(p.id);
    try {
      await api(`/api/admin/printers/${p.id}`, {
        method: "PATCH",
        body: JSON.stringify({is_enabled: !p.is_enabled}),
      });
      notifySuccess(p.is_enabled ? "已停用" : "已开放", p.display_name);
      await load();
    } catch (e) {
      notifyError("修改失败", e instanceof Error ? e.message : "");
    } finally {
      setBusy("");
    }
  }

  async function remove(p: Printer) {
    const ok = window.confirm(
      `删除打印机「${p.display_name}」？\n\n还在排队或正在打印的任务会失败，已完成的打印记录会保留。`,
    );
    if (!ok) return;
    setBusy(p.id);
    try {
      await api(`/api/admin/printers/${p.id}`, {method: "DELETE"});
      notifySuccess("已删除打印机", p.display_name);
      await load();
    } catch (e) {
      notifyError("删除失败", e instanceof Error ? e.message : "");
    } finally {
      setBusy("");
    }
  }

  const enabled = printers.filter((p) => p.is_enabled).length;

  return (
    <div className="rise space-y-4">
      <div className="flex flex-wrap items-center gap-2">
        <Button isIconOnly aria-label="返回" size="sm" variant="ghost" onPress={onBack}>
          <ArrowLeftIcon size={18} />
        </Button>
        <div className="min-w-0 flex-1">
          <h1 className="text-xl font-semibold tracking-tight">打印机管理</h1>
          <p className="mt-0.5 text-xs text-muted">
            游客只能使用已开放的打印机；添加和删除只对管理员开放。
          </p>
        </div>
        <Button isIconOnly aria-label="刷新" size="sm" variant="ghost" onPress={() => void load()}>
          <RefreshIcon size={17} />
        </Button>
        <Button size="sm" onPress={onAdd}>
          <PlusIcon size={15} />
          扫描添加打印机
        </Button>
      </div>

      {error ? (
        <div className="rounded-xl border border-danger/30 bg-danger/8 px-4 py-3 text-sm text-danger">
          {error}
        </div>
      ) : null}

      <section className="overflow-hidden rounded-2xl border border-separator/60 bg-surface/60">
        <header className="flex items-center gap-2 border-b border-separator/50 px-4 py-2.5">
          <span className="text-xs font-medium">已添加的打印机</span>
          <span className="mono ml-auto text-[11px] text-muted">
            共 {printers.length} 台 · 开放 {enabled} 台
          </span>
        </header>

        {loading ? (
          <div className="grid place-items-center py-16">
            <Spinner size="sm" />
          </div>
        ) : printers.length === 0 ? (
          <div className="flex flex-col items-center gap-3 px-6 py-14 text-center">
            <div className="grid size-12 place-items-center rounded-xl bg-default/40 text-muted">
              <PrinterIcon size={22} />
            </div>
            <p className="text-sm text-muted">还没有添加打印机</p>
            <Button size="sm" onPress={onAdd}>
              <PlusIcon size={15} />
              扫描添加打印机
            </Button>
          </div>
        ) : (
          <ul className="divide-y divide-separator/50">
            {printers.map((p) => {
              const endpoints = p.endpoints ?? [];
              const ep =
                endpoints.find((e) => e.is_enabled && e.kind === "cups") ??
                endpoints.find((e) => e.is_enabled) ??
                endpoints[0];
              const health = ep?.health_status ?? "unknown";
              const h = healthLabel[health] ?? healthLabel.unknown;
              const connected = endpoints.some((e) => e.is_enabled);
              return (
                <li
                  className="flex flex-col gap-3 px-4 py-3 sm:flex-row sm:items-center"
                  key={p.id}
                >
                  <button
                    className="group flex min-w-0 flex-1 items-center gap-3 text-left"
                    onClick={() => onOpen(p.id)}
                    type="button"
                  >
                    <span className="grid size-9 shrink-0 place-items-center rounded-lg bg-accent/10 text-accent">
                      <PrinterIcon size={17} />
                    </span>
                    <span className="min-w-0 flex-1">
                      <span className="flex items-center gap-2">
                        <span className="truncate text-sm font-medium">{p.display_name}</span>
                        {!p.is_enabled ? (
                          <Chip color="default" size="sm" variant="soft">
                            已停用
                          </Chip>
                        ) : null}
                      </span>
                      <span className="mono mt-0.5 block truncate text-[11px] text-muted">
                        {[
                          p.model || "型号未知",
                          p.location,
                          p.network_address,
                          ep?.queue_name
                            ? `队列 ${ep.queue_name}`
                            : ep
                              ? endpointKindLabel[ep.kind]
                              : "",
                        ]
                          .filter(Boolean)
                          .join(" · ")}
                      </span>
                    </span>
                    <span className="text-muted/50 transition group-hover:translate-x-0.5 group-hover:text-accent">
                      <ChevronRightIcon size={16} />
                    </span>
                  </button>

                  <div className="flex shrink-0 items-center gap-1.5">
                    {connected ? (
                      <Chip color={h.color} size="sm" variant="soft">
                        {h.label}
                      </Chip>
                    ) : (
                      <Chip color="warning" size="sm" variant="soft">
                        未接入
                      </Chip>
                    )}
                    <Button
                      isDisabled={busy === p.id}
                      size="sm"
                      variant="ghost"
                      onPress={() => void toggle(p)}
                    >
                      {p.is_enabled ? "停用" : "开放"}
                    </Button>
                    <Button
                      isIconOnly
                      aria-label={`删除 ${p.display_name}`}
                      className="text-danger"
                      isDisabled={busy === p.id}
                      size="sm"
                      variant="ghost"
                      onPress={() => void remove(p)}
                    >
                      {busy === p.id ? <Spinner size="sm" /> : <TrashIcon size={16} />}
                    </Button>
                  </div>
                </li>
              );
            })}
          </ul>
        )}
      </section>
    </div>
  );
}
