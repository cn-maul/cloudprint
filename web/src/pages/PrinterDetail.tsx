import {useCallback, useEffect, useRef, useState} from "react";
import {Button, Chip, Input, Spinner} from "@heroui/react";
import {
  api,
  fmt,
  jobStatus,
  printerStateLabel,
  type Job,
  type PrinterDetail,
  type UploadResult,
} from "../lib/api";
import {notifyError, notifySuccess, notifyWarning} from "../lib/toast";
import {
  AlertIcon,
  ArrowLeftIcon,
  CheckIcon,
  CloseIcon,
  CopyIcon,
  FileIcon,
  ListIcon,
  MinusIcon,
  PlusIcon,
  PrinterIcon,
  RefreshIcon,
  StopIcon,
  TrashIcon,
  UploadIcon,
} from "../components/Icon";

// Terminal states only. "queued", "claimed" and "printing" are still live and
// are never cleared — the backend enforces the same rule.
const FINISHED_STATUSES = ["completed", "failed", "canceled"];

export default function PrinterDetailPage({
  printerId,
  onBack,
}: {
  printerId: string;
  onBack: () => void;
}) {
  const [detail, setDetail] = useState<PrinterDetail | null>(null);
  const [error, setError] = useState("");
  const [file, setFile] = useState<UploadResult | null>(null);
  const [pages, setPages] = useState("");
  const [copies, setCopies] = useState(1);
  const [uploading, setUploading] = useState(false);
  const [submitting, setSubmitting] = useState(false);
  const [drag, setDrag] = useState(false);
  const inputRef = useRef<HTMLInputElement>(null);

  const load = useCallback(async () => {
    try {
      const r = await api<PrinterDetail>(`/api/printers/${printerId}`);
      setDetail(r);
      setError("");
    } catch (e) {
      setError(e instanceof Error ? e.message : "载入失败");
    }
  }, [printerId]);

  useEffect(() => {
    void load();
    const t = setInterval(() => void load(), 5000);
    const es = new EventSource("/api/jobs/events");
    const refresh = () => void load();
    ["job.created", "job.completed", "job.failed", "job.printing", "job.canceled", "job.waiting", "job.claimed"].forEach(
      (ev) => es.addEventListener(ev, refresh),
    );
    return () => {
      clearInterval(t);
      es.close();
    };
  }, [load]);

  async function upload(f: File | undefined) {
    if (!f) return;
    setUploading(true);
    try {
      const form = new FormData();
      form.append("file", f);
      const res = await api<UploadResult>("/api/files", {method: "POST", body: form});
      setFile(res);
      setPages("");
    } catch (e) {
      setFile(null);
      notifyError("上传失败", e instanceof Error ? e.message : "");
    } finally {
      setUploading(false);
    }
  }

  function clearFile() {
    setFile(null);
    setPages("");
    if (inputRef.current) inputRef.current.value = "";
  }

  async function submit() {
    if (!file || !detail) return;
    setSubmitting(true);
    try {
      await api<Job>("/api/jobs", {
        method: "POST",
        body: JSON.stringify({
          file_id: file.file_id,
          printer_id: detail.printer.id,
          copies,
          duplex: "one-sided",
          color_mode: "monochrome",
          paper_size: "A4",
          page_range: pages.trim(),
        }),
      });
      notifySuccess("已加入队列");
      clearFile();
      setCopies(1);
      await load();
    } catch (e) {
      const msg = e instanceof Error ? e.message : "提交失败";
      const code = (e as Error & {code?: string}).code;
      if (code === "PAGE_RANGE_INVALID") notifyWarning("页码不对", msg);
      else if (code === "PRINTER_OFFLINE") notifyWarning("打印机连不上", msg);
      else notifyError("发送失败", msg);
    } finally {
      setSubmitting(false);
    }
  }

  async function cancel(j: Job) {
    if (!window.confirm(`取消“${j.file_name || "这个任务"}”？`)) return;
    try {
      await api(`/api/jobs/${j.id}/cancel`, {method: "POST"});
      await load();
    } catch (e) {
      notifyError("取消失败", e instanceof Error ? e.message : "");
    }
  }

  // Clearing is only about tidying the queue view: the backend refuses to
  // delete anything still queued, printing or on its way to the device.
  async function clearFinished() {
    const n = finishedCount;
    if (n === 0) return;
    if (!window.confirm(`清除 ${n} 条已结束的任务记录？正在打印和排队的任务不受影响。`)) return;
    try {
      const r = await api<{removed: number}>(`/api/printers/${printerId}/jobs/clear`, {method: "POST"});
      await load();
      notifySuccess(`已清除 ${r.removed} 条记录`);
    } catch (e) {
      notifyError("清除失败", e instanceof Error ? e.message : "");
    }
  }

  if (!detail) {
    return (
      <div className="grid min-h-[40vh] place-items-center">
        {error ? (
          <span className="flex items-center gap-2 text-sm text-danger">
            <AlertIcon size={16} />
            {error}
          </span>
        ) : (
          <Spinner size="sm" />
        )}
      </div>
    );
  }

  const {printer, status, jobs} = detail;
  const totalPages = file ? countPages(pages, file.page_count) : 0;
  const copiesMax = Math.max(1, Math.min(99, printer.endpoints?.[0]?.capabilities?.max_copies ?? 99));
  const offline = status.state === "stopped" || status.state === "unknown";
  const problem = isProblem(status.message);
  const finishedCount = jobs.filter((j) => FINISHED_STATUSES.includes(j.status)).length;

  return (
    <div className="space-y-5">
      <div className="flex items-center gap-3">
        <Button isIconOnly aria-label="返回" size="sm" variant="ghost" onPress={onBack}>
          <ArrowLeftIcon size={18} />
        </Button>
        <div className="relative grid size-11 place-items-center rounded-xl bg-accent/10 text-accent">
          <PrinterIcon size={21} />
          <span
            className={`absolute -right-0.5 -bottom-0.5 size-2.5 rounded-full ring-2 ring-surface ${
              offline ? "bg-danger" : status.state === "processing" ? "bg-warning" : "bg-success"
            }`}
          />
        </div>
        <div className="min-w-0 flex-1">
          <h1 className="truncate text-lg font-semibold tracking-tight">{printer.display_name}</h1>
          <div className="truncate text-xs text-muted">
            {[printer.location, printer.model || status.model, printer.network_address].filter(Boolean).join(" · ")}
          </div>
        </div>
        <StateChip status={status} />
        <Button isIconOnly aria-label="刷新" size="sm" variant="ghost" onPress={() => void load()}>
          <RefreshIcon size={17} />
        </Button>
      </div>

      {problem ? (
        <div className="flex items-center gap-2 rounded-xl border border-warning/30 bg-warning/8 px-4 py-3 text-sm text-warning">
          <AlertIcon size={16} />
          <span className="font-medium">{status.message}</span>
        </div>
      ) : null}

      <div className="grid gap-5 lg:grid-cols-[minmax(0,1fr)_360px]">
        <section className="overflow-hidden rounded-2xl border border-separator/60 bg-surface/60">
          <header className="flex items-center gap-2 border-b border-separator/50 px-5 py-3 text-muted">
            <ListIcon size={16} />
            <span className="mono text-xs">{jobs.length}</span>
            {finishedCount > 0 ? (
              <Button
                className="ml-auto"
                size="sm"
                variant="ghost"
                onPress={() => void clearFinished()}
              >
                <TrashIcon size={15} />
                清除
              </Button>
            ) : null}
          </header>
          {jobs.length === 0 ? (
            <div className="grid place-items-center py-20 text-muted/40">
              <ListIcon size={26} />
            </div>
          ) : (
            <ul className="divide-y divide-separator/50">
              {jobs.map((j) => (
                <JobRow key={j.id} job={j} onCancel={() => void cancel(j)} />
              ))}
            </ul>
          )}
        </section>

        <section className="space-y-4 self-start rounded-2xl border border-separator/60 bg-surface/60 p-4">
          <input
            accept=".pdf,.jpg,.jpeg,.png"
            className="hidden"
            onChange={(e) => void upload(e.target.files?.[0])}
            ref={inputRef}
            type="file"
          />
          <div
            className={`relative flex cursor-pointer flex-col items-center justify-center gap-2 rounded-xl border-2 border-dashed px-4 py-7 transition ${
              drag ? "border-accent bg-accent/8" : "border-separator/80 hover:border-accent/50"
            }`}
            onClick={() => !uploading && inputRef.current?.click()}
            onDragEnter={(e) => {
              e.preventDefault();
              setDrag(true);
            }}
            onDragLeave={() => setDrag(false)}
            onDragOver={(e) => e.preventDefault()}
            onDrop={(e) => {
              e.preventDefault();
              setDrag(false);
              void upload(e.dataTransfer.files?.[0]);
            }}
            role="button"
            tabIndex={0}
          >
            {uploading ? (
              <Spinner size="sm" />
            ) : file ? (
              <>
                <span className="text-accent">
                  <FileIcon size={24} />
                </span>
                <span className="max-w-full truncate px-6 text-sm font-medium">{file.name}</span>
                <span className="mono text-[11px] text-muted">
                  {file.page_count} 页 · {fmt.bytes(file.size_bytes)}
                </span>
                <button
                  aria-label="移除文件"
                  className="absolute top-2 right-2 grid size-7 place-items-center rounded-full text-muted transition hover:bg-default/60 hover:text-danger"
                  onClick={(e) => {
                    e.stopPropagation();
                    clearFile();
                  }}
                  type="button"
                >
                  <CloseIcon size={14} />
                </button>
              </>
            ) : (
              <span className="text-muted">
                <UploadIcon size={26} />
              </span>
            )}
          </div>

          <div className="flex items-center gap-2">
            <span className="grid size-9 shrink-0 place-items-center rounded-lg bg-default/50 text-muted">
              <ListIcon size={16} />
            </span>
            <Input
              className="mono"
              fullWidth
              disabled={!file}
              placeholder={file ? `1-${file.page_count}` : ""}
              value={pages}
              onChange={(e) => setPages(e.target.value)}
            />
            <span className="mono w-10 shrink-0 text-right text-xs text-muted">{file ? totalPages : 0}</span>
          </div>

          <div className="flex items-center gap-2">
            <span className="grid size-9 shrink-0 place-items-center rounded-lg bg-default/50 text-muted">
              <CopyIcon size={16} />
            </span>
            <Button isIconOnly size="sm" variant="secondary" onPress={() => setCopies((c) => Math.max(1, c - 1))}>
              <MinusIcon size={15} />
            </Button>
            <div className="mono grid h-9 flex-1 place-items-center rounded-lg border border-separator bg-surface text-sm">
              {copies}
            </div>
            <Button
              isIconOnly
              size="sm"
              variant="secondary"
              onPress={() => setCopies((c) => Math.min(copiesMax, c + 1))}
            >
              <PlusIcon size={15} />
            </Button>
          </div>

          <div className="flex items-center justify-between border-t border-separator/60 pt-1 text-sm">
            <span className="text-muted">
              <CheckIcon size={15} />
            </span>
            <span className="mono text-xs text-muted">
              {totalPages} × {copies} = <span className="text-foreground">{file ? totalPages * copies : 0}</span>
            </span>
          </div>

          <Button
            fullWidth
            isDisabled={!file || submitting || offline}
            size="lg"
            onPress={() => void submit()}
          >
            <PrinterIcon size={18} />
            打印
          </Button>
        </section>
      </div>
    </div>
  );
}

function StateChip({status}: {status: PrinterDetail["status"]}) {
  const map: Record<string, {label: string; color: "success" | "warning" | "danger" | "default"}> = {
    idle: {label: printerStateLabel.idle, color: "success"},
    processing: {label: printerStateLabel.processing, color: "warning"},
    printing: {label: printerStateLabel.processing, color: "warning"},
    stopped: {label: "当前离线", color: "danger"},
  };
  const s = map[status.state] ?? {label: printerStateLabel.unknown, color: "default" as const};
  return (
    <Chip color={s.color} size="sm" variant="soft">
      {s.label}
    </Chip>
  );
}

// "就绪" and "正在打印" describe normal operation; anything else the backend
// reports is a condition the user has to act on.
function isProblem(message?: string) {
  return !!message && !["就绪", "正在打印", "未知"].includes(message);
}

function JobRow({job, onCancel}: {job: Job; onCancel: () => void}) {
  const st = jobStatus[job.status] ?? {label: job.status, tone: "text-muted"};
  const active = ["queued", "claimed", "printing"].includes(job.status);
  return (
    <li className="flex items-center gap-3 px-4 py-3">
      <span className="grid size-9 shrink-0 place-items-center rounded-lg bg-default/40 text-muted">
        <FileIcon size={17} />
      </span>
      <div className="min-w-0 flex-1">
        <div className="truncate text-sm">{job.file_name || "打印文件"}</div>
        <div className="mono mt-0.5 truncate text-[11px] text-muted">
          {job.page_range ? `${job.page_range} · ` : ""}
          {job.total_pages} 页 × {job.copies} · {fmt.date(job.created_at)}
        </div>
        {job.error_message && job.status !== "completed" ? (
          <div className="mt-0.5 truncate text-[11px] text-danger" title={job.error_message}>
            {job.error_message}
          </div>
        ) : null}
      </div>
      <span className={`shrink-0 text-xs ${st.tone}`}>{st.label}</span>
      {active ? (
        <button
          aria-label="取消"
          className="shrink-0 text-muted/70 transition hover:text-danger"
          onClick={onCancel}
          type="button"
        >
          <StopIcon size={15} />
        </button>
      ) : (
        <span className="w-[15px] shrink-0" />
      )}
    </li>
  );
}

function countPages(raw: string, total: number) {
  const text = raw.replace(/，/g, ",").trim();
  if (!text) return total;
  const seen = new Set<number>();
  for (const part of text.split(",")) {
    const [a, b] = part.split("-");
    const from = Number(a) || 1;
    const to = b === undefined ? from : b === "" ? total : Number(b);
    if (from >= 1 && to >= from && to <= total) {
      for (let p = from; p <= to; p++) seen.add(p);
    }
  }
  return seen.size;
}
