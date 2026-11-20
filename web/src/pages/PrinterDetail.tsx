import {useCallback, useEffect, useRef, useState} from "react";
import {Button, Chip, Input, Spinner} from "@heroui/react";
import {
  api,
  fmt,
  jobStatus,
  printerStateLabel,
  type Job,
  type PrinterDetail,
  type PrinterOptions,
  type UploadResult,
} from "../lib/api";
import {notifyError, notifySuccess, notifyWarning} from "../lib/toast";
import {
  AlertIcon,
  ArrowLeftIcon,
  ArrowRightIcon,
  ChevronDownIcon,
  ChevronRightIcon,
  CloseIcon,
  CopyIcon,
  FileIcon,
  ListIcon,
  MinusIcon,
  PlusIcon,
  PrinterIcon,
  RefreshIcon,
  RetryIcon,
  StopIcon,
  TrashIcon,
  UploadIcon,
} from "../components/Icon";

const FINISHED_STATUSES = ["completed", "failed", "canceled"];
const PAGE_SIZE = 20;

type FilterKey = "all" | "active" | "done" | "problem";

const FILTERS: Array<{key: FilterKey; label: string; status?: string}> = [
  {key: "all", label: "全部"},
  {key: "active", label: "进行中", status: "queued,claimed,printing"},
  {key: "done", label: "已完成", status: "completed"},
  {key: "problem", label: "异常", status: "failed,canceled"},
];

export default function PrinterDetailPage({
  printerId,
  onBack,
}: {
  printerId: string;
  onBack: () => void;
}) {
  const [detail, setDetail] = useState<PrinterDetail | null>(null);
  const [options, setOptions] = useState<PrinterOptions | null>(null);
  const [error, setError] = useState("");
  const [filter, setFilter] = useState<FilterKey>("all");
  const [offset, setOffset] = useState(0);
  const [file, setFile] = useState<UploadResult | null>(null);
  const [previewFailed, setPreviewFailed] = useState(false);
  const [previewPage, setPreviewPage] = useState(1);
  const [pages, setPages] = useState("");
  const [copies, setCopies] = useState(1);
  const [queueOpen, setQueueOpen] = useState(true);
  const [uploading, setUploading] = useState(false);
  const [submitting, setSubmitting] = useState(false);
  const [drag, setDrag] = useState(false);
  const inputRef = useRef<HTMLInputElement>(null);
  const pending = useRef(false);

  const load = useCallback(async () => {
    const q = new URLSearchParams({limit: String(PAGE_SIZE), offset: String(offset)});
    const st = FILTERS.find((f) => f.key === filter)?.status;
    if (st) q.set("status", st);
    try {
      const r = await api<PrinterDetail>(`/api/printers/${printerId}?${q.toString()}`);
      setDetail(r);
      setError("");
    } catch (e) {
      setError(e instanceof Error ? e.message : "载入失败");
    }
  }, [printerId, filter, offset]);

  const loadOptions = useCallback(async () => {
    try {
      const r = await api<PrinterOptions>(`/api/printers/${printerId}/options`);
      setOptions(r);
    } catch {
      setOptions(null);
    }
  }, [printerId]);

  useEffect(() => {
    void load();
  }, [load]);

  useEffect(() => {
    void loadOptions();
    // Only on printer change; paper options are stable while the page is open.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [printerId]);

  useEffect(() => {
    const t = setInterval(() => void load(), 4000);
    const es = new EventSource("/api/jobs/events");
    const refresh = () => {
      if (pending.current) return;
      pending.current = true;
      window.setTimeout(() => {
        pending.current = false;
        void load();
      }, 400);
    };
    ["job.created", "job.completed", "job.failed", "job.printing", "job.canceled", "job.waiting"].forEach((ev) =>
      es.addEventListener(ev, refresh),
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
      setPreviewFailed(false);
      setPreviewPage(1);
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
    setPreviewFailed(false);
    setPreviewPage(1);
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
      setOffset(0);
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
    try {
      await api(`/api/jobs/${j.id}/cancel`, {method: "POST"});
      notifySuccess("已取消");
      await load();
    } catch (e) {
      notifyError("取消失败", e instanceof Error ? e.message : "");
    }
  }

  async function retry(j: Job) {
    try {
      await api(`/api/jobs/${j.id}/retry`, {method: "POST"});
      notifySuccess("已重新排队");
      await load();
    } catch (e) {
      notifyError("重试失败", e instanceof Error ? e.message : "");
    }
  }

  async function reprint(j: Job) {
    try {
      await api(`/api/jobs/${j.id}/reprint`, {method: "POST"});
      notifySuccess("已重新排队");
      await load();
    } catch (e) {
      notifyError("再打失败", e instanceof Error ? e.message : "");
    }
  }

  async function clearFinished() {
    if (finishedCount === 0) return;
    if (!window.confirm(`清除 ${finishedCount} 条已结束的任务记录？正在打印和排队的任务不受影响。`)) return;
    try {
      const r = await api<{removed: number}>(`/api/printers/${printerId}/jobs/clear`, {method: "POST"});
      setOffset(0);
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

  const {printer, status, jobs, total} = detail;
  const totalPages = file ? countPages(pages, file.page_count) : 0;
  const copiesMax = Math.max(1, Math.min(99, options?.copies_max ?? 99));
  const offline = status.state === "stopped" || status.state === "unknown";
  const problem = isProblem(status.message);
  const finishedCount = jobs.filter((j) => FINISHED_STATUSES.includes(j.status)).length;
  const activeJob = jobs.find((j) => j.status === "printing");

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

      <input
        accept=".pdf,.jpg,.jpeg,.png"
        className="hidden"
        onChange={(e) => void upload(e.target.files?.[0])}
        ref={inputRef}
        type="file"
      />

      <div className="grid gap-4 lg:grid-cols-[minmax(0,1fr)_400px] lg:items-start">
        <section
          className={`flex min-h-[420px] flex-col overflow-hidden rounded-2xl border border-separator/60 bg-surface/60 lg:h-[calc(100vh-215px)] ${
            drag ? "ring-2 ring-accent/50" : ""
          }`}
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
          tabIndex={file ? 0 : -1}
          onKeyDown={(e) => {
            if (!file) return;
            if (e.key === "ArrowLeft") {
              e.preventDefault();
              setPreviewPage((p) => Math.max(1, p - 1));
            }
            if (e.key === "ArrowRight") {
              e.preventDefault();
              setPreviewPage((p) => Math.min(file.page_count, p + 1));
            }
          }}
        >
          <header className="flex items-center gap-2 border-b border-separator/50 px-3 py-2">
            <span className={`grid size-7 place-items-center ${file ? "text-accent" : "text-muted"}`}>
              <FileIcon size={15} />
            </span>
            <span className="min-w-0 flex-1 truncate text-xs">{file ? file.name : "未选择文件"}</span>
            {file ? (
              <>
                <span className="mono shrink-0 rounded bg-default/50 px-1.5 py-0.5 text-[10px] text-muted">A4</span>
                <Button isIconOnly aria-label="移除文件" size="sm" variant="ghost" onPress={clearFile}>
                  <CloseIcon size={14} />
                </Button>
              </>
            ) : null}
          </header>

          <div className="relative min-h-0 flex-1 overflow-hidden bg-default/20">
            {uploading ? (
              <div className="absolute inset-0 grid place-items-center">
                <Spinner size="sm" />
              </div>
            ) : file && !previewFailed ? (
              <div className="absolute inset-0 flex items-center justify-center p-5">
                <img
                  alt={`${file.name} 第 ${previewPage} 页`}
                  className="max-h-full max-w-full rounded-[2px] bg-white object-contain shadow-[0_8px_24px_-10px_rgba(0,0,0,0.45)]"
                  onError={() => setPreviewFailed(true)}
                  src={`${file.preview_url}?page=${previewPage}`}
                />
              </div>
            ) : file ? (
              <div className="absolute inset-0 grid place-items-center text-muted">
                <FileIcon size={30} />
              </div>
            ) : (
              <div className="absolute inset-0 grid place-items-center p-6">
                <button
                  className={`flex w-full max-w-md flex-col items-center justify-center gap-3 rounded-xl border-2 border-dashed px-6 py-16 text-muted transition ${
                    drag
                      ? "border-accent text-accent"
                      : "border-separator/80 hover:border-accent/50 hover:text-accent"
                  }`}
                  onClick={() => inputRef.current?.click()}
                  type="button"
                >
                  <UploadIcon size={30} />
                  <span className="text-sm">选择文件</span>
                </button>
              </div>
            )}
          </div>

          {file ? (
            <footer className="flex shrink-0 items-center justify-center gap-1 border-t border-separator/50 bg-surface/60 py-1">
              <Button
                isIconOnly
                aria-label="上一页"
                isDisabled={previewPage <= 1}
                size="sm"
                variant="ghost"
                onPress={() => setPreviewPage((p) => Math.max(1, p - 1))}
              >
                <ArrowLeftIcon size={15} />
              </Button>
              <span className="mono min-w-14 text-center text-[11px] text-muted">
                {previewPage} / {file.page_count}
              </span>
              <Button
                isIconOnly
                aria-label="下一页"
                isDisabled={previewPage >= file.page_count}
                size="sm"
                variant="ghost"
                onPress={() => setPreviewPage((p) => Math.min(file.page_count, p + 1))}
              >
                <ChevronRightIcon size={15} />
              </Button>
            </footer>
          ) : null}
        </section>

        <div className="flex min-w-0 flex-col gap-4 lg:h-[calc(100vh-215px)]">
          <section
            className={`overflow-hidden rounded-2xl border border-separator/60 bg-surface/60 ${
              queueOpen ? "flex min-h-0 flex-col lg:flex-1" : "shrink-0"
            }`}
          >
            {queueOpen ? (
              <>
              <header className="flex items-center gap-1.5 border-b border-separator/50 px-2 py-2.5">
                {FILTERS.map((f) => (
                  <button
                    className={`rounded-lg px-1.5 py-1 text-xs transition ${
                      filter === f.key
                        ? "bg-accent/12 font-medium text-accent"
                        : "text-muted hover:bg-default/50 hover:text-foreground"
                    }`}
                    key={f.key}
                    onClick={() => {
                      setFilter(f.key);
                      setOffset(0);
                    }}
                    type="button"
                  >
                    {f.label}
                  </button>
                ))}
                <span className="mono ml-auto text-[11px] text-muted">{total}</span>
                {finishedCount > 0 ? (
                  <Button
                    isIconOnly
                    aria-label="清除已结束任务"
                    size="sm"
                    variant="ghost"
                    onPress={() => void clearFinished()}
                  >
                    <TrashIcon size={15} />
                  </Button>
                ) : null}
                <Button
                  isIconOnly
                  aria-label="收起列表"
                  size="sm"
                  variant="ghost"
                  onPress={() => setQueueOpen(false)}
                >
                  <ArrowRightIcon size={15} />
                </Button>
              </header>
              <div className="max-h-[38vh] min-h-0 overflow-auto scroll-thin lg:max-h-none lg:flex-1">
                {jobs.length === 0 ? (
                  <div className="grid place-items-center py-16 text-muted/40">
                    <ListIcon size={24} />
                  </div>
                ) : (
                  <ul className="divide-y divide-separator/50">
                    {jobs.map((j) => (
                      <JobRow
                        key={j.id}
                        job={j}
                        onCancel={() => void cancel(j)}
                        onReprint={() => void reprint(j)}
                        onRetry={() => void retry(j)}
                      />
                    ))}
                  </ul>
                )}
              </div>
              {total > PAGE_SIZE ? (
                <div className="flex items-center justify-between border-t border-separator/50 px-3 py-2">
                  <Button
                    isDisabled={offset === 0}
                    size="sm"
                    variant="ghost"
                    onPress={() => setOffset((o) => Math.max(0, o - PAGE_SIZE))}
                  >
                    <ArrowLeftIcon size={15} />
                  </Button>
                  <span className="mono text-[11px] text-muted">
                    {offset + 1}–{Math.min(offset + jobs.length, total)} / {total}
                  </span>
                  <Button
                    isDisabled={offset + PAGE_SIZE >= total}
                    size="sm"
                    variant="ghost"
                    onPress={() => setOffset((o) => o + PAGE_SIZE)}
                  >
                    <ChevronRightIcon size={15} />
                  </Button>
                </div>
              ) : null}
              </>
            ) : (
              <button
                aria-label="展开列表"
                className="flex w-full items-center gap-2 px-3 py-2.5 text-muted transition hover:text-accent"
                onClick={() => setQueueOpen(true)}
                type="button"
              >
                <ListIcon size={15} />
                <span className="text-xs">队列</span>
                <span className="mono ml-auto text-[11px] tabular-nums">{total}</span>
                {activeJob ? <span className="size-1.5 animate-pulse rounded-full bg-warning" /> : null}
                <ChevronDownIcon size={15} />
              </button>
            )}
          </section>

          <section className="mt-auto shrink-0 space-y-3 rounded-2xl border border-separator/60 bg-surface/60 p-4">
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
              <span className="mono w-24 shrink-0 text-right text-xs text-muted">
                {totalPages} × {copies} = <span className="text-foreground">{file ? totalPages * copies : 0}</span>
              </span>
            </div>

            <Button fullWidth isDisabled={!file || submitting || offline} size="lg" onPress={() => void submit()}>
              <PrinterIcon size={18} />
              打印
            </Button>

            {activeJob?.progress ? (
              <div className="space-y-1.5 border-t border-separator/60 pt-2">
                <div className="flex items-center justify-between text-[11px] text-muted">
                  <span>正在打印</span>
                  <span className="mono">
                    {activeJob.progress.printed}/{activeJob.progress.total}
                  </span>
                </div>
                <div className="h-1.5 overflow-hidden rounded-full bg-default/60">
                  <div
                    className="h-full rounded-full bg-accent transition-all"
                    style={{
                      width: `${Math.min(100, Math.round((activeJob.progress.printed / Math.max(1, activeJob.progress.total)) * 100))}%`,
                    }}
                  />
                </div>
              </div>
            ) : null}
          </section>
        </div>
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

function isProblem(message?: string) {
  return !!message && !["就绪", "正在打印", "未知"].includes(message);
}

function JobRow({
  job,
  onCancel,
  onRetry,
  onReprint,
}: {
  job: Job;
  onCancel: () => void;
  onRetry: () => void;
  onReprint: () => void;
}) {
  const st = jobStatus[job.status] ?? {label: job.status, tone: "text-muted"};
  const active = ["queued", "claimed", "printing"].includes(job.status);
  const progress = job.progress;
  const pct = progress ? Math.min(100, Math.round((progress.printed / Math.max(1, progress.total)) * 100)) : 0;
  return (
    <li className="flex items-center gap-3 px-4 py-3">
      <span className="grid size-9 shrink-0 place-items-center rounded-lg bg-default/40 text-muted">
        <FileIcon size={17} />
      </span>
      <div className="min-w-0 flex-1">
        <div className="flex items-center gap-1.5">
          <span className="truncate text-sm">{job.file_name || "打印文件"}</span>
          {job.retry_of ? (
            <span className="shrink-0 rounded bg-default/60 px-1 py-px text-[10px] text-muted">
              <RetryIcon size={10} />
            </span>
          ) : null}
        </div>
        <div className="mono mt-0.5 truncate text-[11px] text-muted">
          {job.page_range ? `${job.page_range} · ` : ""}
          {job.total_pages} 页 × {job.copies} · {fmt.date(job.created_at)}
        </div>
        {job.status === "printing" && progress ? (
          <div className="mt-1.5 flex items-center gap-2">
            <div className="h-1 w-24 overflow-hidden rounded-full bg-default/50">
              <div className="h-full rounded-full bg-accent transition-all" style={{width: `${pct}%`}} />
            </div>
            <span className="mono text-[10px] text-muted">
              {progress.printed}/{progress.total}
            </span>
          </div>
        ) : null}
        {job.error_message && job.status !== "completed" ? (
          <div className="mt-0.5 truncate text-[11px] text-danger" title={job.error_message}>
            {job.error_message}
          </div>
        ) : null}
      </div>
      <span className={`shrink-0 text-xs ${st.tone}`}>{st.label}</span>
      <div className="flex w-8 shrink-0 items-center justify-end">
        {active ? (
          <button
            aria-label="取消"
            className="text-muted/70 transition hover:text-danger"
            onClick={onCancel}
            title="取消"
            type="button"
          >
            <StopIcon size={15} />
          </button>
        ) : job.status === "failed" || job.status === "canceled" ? (
          <button
            aria-label="重试"
            className="text-muted/70 transition hover:text-accent"
            onClick={onRetry}
            title="重试"
            type="button"
          >
            <RetryIcon size={16} />
          </button>
        ) : job.status === "completed" ? (
          <button
            aria-label="再打一份"
            className="text-muted/70 transition hover:text-accent"
            onClick={onReprint}
            title="再打一份"
            type="button"
          >
            <RetryIcon size={16} />
          </button>
        ) : null}
      </div>
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
