import {useCallback, useEffect, useState} from "react";
import {Chip, Spinner} from "@heroui/react";
import {api, printerStateLabel, type Printer, type PrinterQueueCount} from "../lib/api";
import {ChevronRightIcon, PlusCircleIcon, PrinterIcon} from "../components/Icon";

export default function PrintersPage({
  onOpen,
  onAdd,
  canAdd,
}: {
  onOpen: (id: string) => void;
  onAdd: () => void;
  // Registering a printer is an admin task; guests only pick from what exists.
  canAdd: boolean;
}) {
  const [printers, setPrinters] = useState<Printer[]>([]);
  const [queues, setQueues] = useState<Record<string, PrinterQueueCount>>({});
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");

  const load = useCallback(async () => {
    try {
      const r = await api<{printers: Printer[]; queues?: Record<string, PrinterQueueCount>}>("/api/printers");
      setPrinters((r.printers ?? []).map((p) => ({...p, endpoints: p.endpoints ?? []})));
      setQueues(r.queues ?? {});
      setError("");
    } catch (e) {
      setError(e instanceof Error ? e.message : "载入失败");
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void load();
    const t = setInterval(() => void load(), 10000);
    return () => clearInterval(t);
  }, [load]);

  if (loading) {
    return (
      <div className="grid min-h-[50vh] place-items-center">
        <Spinner size="sm" />
      </div>
    );
  }

  if (error) {
    return (
      <div className="flex items-center gap-2 rounded-2xl border border-danger/30 bg-danger/8 px-4 py-3 text-sm text-danger">
        {error}
      </div>
    );
  }

  if (printers.length === 0) {
    return (
      <div className="grid min-h-[50vh] place-items-center">
        {canAdd ? (
          <button
            className="group grid place-items-center gap-3 rounded-2xl border border-dashed border-separator/80 px-10 py-8 text-muted/60 transition hover:border-accent/50 hover:text-accent"
            onClick={onAdd}
            type="button"
          >
            <PlusCircleIcon size={28} />
          </button>
        ) : (
          <div className="flex flex-col items-center gap-3 text-center">
            <div className="grid size-12 place-items-center rounded-xl bg-default/40 text-muted">
              <PrinterIcon size={22} />
            </div>
            <p className="text-sm text-muted">还没有可用的打印机</p>
            <p className="max-w-xs text-xs text-muted/70">
              请联系管理员添加打印机，或让他把你加入对应的使用分组。
            </p>
          </div>
        )}
      </div>
    );
  }

  return (
    <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-3">
      {printers.map((p) => (
        <PrinterCard key={p.id} printer={p} queue={queues[p.id]} onOpen={() => onOpen(p.id)} />
      ))}
      {canAdd ? (
        <button
          className="flex min-h-[132px] items-center justify-center rounded-2xl border border-dashed border-separator/70 text-muted/50 transition hover:border-accent/50 hover:text-accent"
          onClick={onAdd}
          type="button"
        >
          <PlusCircleIcon size={24} />
        </button>
      ) : null}
    </div>
  );
}

function PrinterCard({
  printer,
  queue,
  onOpen,
}: {
  printer: Printer;
  queue?: PrinterQueueCount;
  onOpen: () => void;
}) {
  const ep = (printer.endpoints ?? []).find((e) => e.kind === "cups") ?? printer.endpoints?.[0];
  const state = ep?.capabilities?.state ?? "";
  const healthy = ep?.health_status === "healthy";
  const label = printerStateLabel[state] ?? (healthy ? printerStateLabel.idle : "未连接");
  const color = !healthy ? "danger" : state === "processing" ? "warning" : "success";
  const active = healthy && state === "processing";

  return (
    <button
      className="group relative flex flex-col gap-4 rounded-2xl border border-separator/60 bg-surface/60 p-5 text-left transition hover:border-accent/50 hover:bg-surface/90"
      onClick={onOpen}
      type="button"
    >
      <div className="flex items-start gap-3">
        <div className="relative grid size-12 shrink-0 place-items-center rounded-xl bg-accent/10 text-accent">
          <PrinterIcon size={22} />
          <span
            className={`absolute -right-0.5 -bottom-0.5 size-2.5 rounded-full ring-2 ring-surface ${
              color === "success" ? "bg-success" : color === "warning" ? "bg-warning" : "bg-danger"
            }`}
          />
        </div>
        <div className="min-w-0 flex-1">
          <div className="truncate pr-6 font-medium">{printer.display_name}</div>
          <div className="mt-0.5 truncate text-xs text-muted">
            {[printer.location, printer.model].filter(Boolean).join(" · ")}
          </div>
        </div>
        <span className="absolute top-5 right-4 text-muted/70 transition group-hover:translate-x-0.5 group-hover:text-accent">
          <ChevronRightIcon size={18} />
        </span>
      </div>

      <div className="flex items-center justify-between gap-3">
        <Chip color={color} size="sm" variant="soft">
          <span className="flex items-center gap-1.5">
            {active ? <span className="size-1.5 animate-pulse rounded-full bg-current" /> : null}
            {label}
          </span>
        </Chip>
        <div className="flex min-w-0 items-center gap-2">
          {queue && queue.queued + queue.printing > 0 ? (
            <span className="mono shrink-0 rounded-md bg-accent/10 px-1.5 py-0.5 text-[10px] text-accent">
              {queue.printing > 0 ? `${queue.printing} 打印` : ""}
              {queue.printing > 0 && queue.queued > 0 ? " · " : ""}
              {queue.queued > 0 ? `${queue.queued} 等待` : ""}
            </span>
          ) : null}
          {printer.network_address ? (
            <span className="mono truncate text-[11px] text-muted/80">{printer.network_address}</span>
          ) : null}
        </div>
      </div>
    </button>
  );
}
