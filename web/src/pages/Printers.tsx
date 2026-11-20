import {useCallback, useEffect, useState} from "react";
import {Chip, Spinner} from "@heroui/react";
import {api, printerStateLabel, type Printer} from "../lib/api";
import {ChevronRightIcon, PrinterIcon} from "../components/Icon";

export default function PrintersPage({onOpen}: {onOpen: (id: string) => void}) {
  const [printers, setPrinters] = useState<Printer[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");

  const load = useCallback(async () => {
    try {
      const r = await api<{printers: Printer[]}>("/api/printers");
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
        <div className="grid size-16 place-items-center rounded-2xl border border-dashed border-separator/80 text-muted/60">
          <PrinterIcon size={28} />
        </div>
      </div>
    );
  }

  return (
    <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-3">
      {printers.map((p) => (
        <PrinterCard key={p.id} printer={p} onOpen={() => onOpen(p.id)} />
      ))}
    </div>
  );
}

function PrinterCard({printer, onOpen}: {printer: Printer; onOpen: () => void}) {
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
        {printer.network_address ? (
          <span className="mono truncate text-[11px] text-muted/80">{printer.network_address}</span>
        ) : null}
      </div>
    </button>
  );
}
