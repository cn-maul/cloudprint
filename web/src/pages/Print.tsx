import {useCallback, useEffect, useRef, useState} from "react";
import {Button, Chip, ListBox, Select} from "@heroui/react";
import {
  api,
  duplexLabel,
  fmt,
  jobStatus,
  type Job,
  type Printer,
  type UploadResult,
  type User,
} from "../lib/api";
import {notifyError, notifySuccess, notifyWarning} from "../lib/toast";
import {EmptyBox, Field, PageHeader, SectionCard} from "../components/ui";

export default function PrintPage({user}: {user: User}) {
  const [printers, setPrinters] = useState<Printer[]>([]);
  const [printerId, setPrinterId] = useState("");
  const [file, setFile] = useState<UploadResult | null>(null);
  const [jobs, setJobs] = useState<Job[]>([]);
  const [copies, setCopies] = useState(1);
  const [usedPages, setUsedPages] = useState(0);
  const [maxUploadMB, setMaxUploadMB] = useState(50);
  const [duplex, setDuplex] = useState<"one-sided" | "two-sided-long-edge" | "two-sided-short-edge">("one-sided");
  const [uploading, setUploading] = useState(false);
  const [submitting, setSubmitting] = useState(false);
  const [drag, setDrag] = useState(false);
  const [loadError, setLoadError] = useState("");
  const inputRef = useRef<HTMLInputElement>(null);

  const load = useCallback(async () => {
    try {
      const [p, j, d] = await Promise.all([
        api<{printers: Printer[]}>("/api/printers"),
        api<{jobs: Job[]}>("/api/jobs?limit=6"),
        api<{daily_pages: number; daily_page_quota: number; max_upload_mb: number}>("/api/dashboard"),
      ]);
      const list = (p.printers ?? []).map((x) => ({...x, endpoints: x.endpoints ?? []}));
      setPrinters(list);
      setJobs(j.jobs ?? []);
      setUsedPages(d.daily_pages ?? 0);
      setMaxUploadMB(d.max_upload_mb || 50);
      setPrinterId((cur) => (cur && list.some((x) => x.id === cur) ? cur : (list[0]?.id ?? "")));
      setLoadError("");
    } catch (e) {
      setLoadError(e instanceof Error ? e.message : "载入失败");
    }
  }, []);

  useEffect(() => {
    void load();
    const es = new EventSource("/api/jobs/events");
    const refresh = () => {
      api<{jobs: Job[]}>("/api/jobs?limit=6").then((r) => setJobs(r.jobs ?? [])).catch(() => {});
    };
    ["job.created", "job.completed", "job.failed", "job.printing", "job.canceled", "job.waiting", "job.claimed"].forEach(
      (ev) => es.addEventListener(ev, refresh),
    );
    return () => es.close();
  }, [load]);

  const selected = printers.find((p) => p.id === printerId);
  const usable = (p?: Printer) =>
    (p?.endpoints ?? []).filter((e) => e.is_enabled && e.health_status === "healthy")[0];
  const endpoint = usable(selected);
  const readyPrinters = printers.filter((p) => !!usable(p));
  const needsSetup = printers.length > 0 && readyPrinters.length === 0;
  const supportsDuplex =
    !duplex || !endpoint?.capabilities.sides?.length || endpoint.capabilities.sides.includes(duplex);
  const total = (file?.page_count || 0) * copies;
  const remaining =
    user.daily_page_quota > 0 ? Math.max(0, user.daily_page_quota - usedPages) : Number.POSITIVE_INFINITY;

  async function doUpload(f: File) {
    setUploading(true);
    try {
      const form = new FormData();
      form.append("file", f);
      const res = await api<UploadResult>("/api/files", {method: "POST", body: form});
      setFile(res);
      notifySuccess("文件已就绪", `${res.name} · ${res.page_count} 页`);
    } catch (e) {
      setFile(null);
      notifyError("上传失败", e instanceof Error ? e.message : "");
    } finally {
      setUploading(false);
    }
  }

  function pick(f: File | undefined) {
    if (!f) return;
    if (f.size > maxUploadMB * 1024 * 1024) {
      notifyError("文件太大", `单个文件不能超过 ${maxUploadMB} MB`);
      return;
    }
    void doUpload(f);
  }

  async function submit() {
    if (!file || !printerId) return;
    if (!endpoint) {
      notifyWarning("这台打印机现在不可用", "请换一台，或联系管理员检查设备。");
      return;
    }
    if (user.daily_page_quota > 0 && total > remaining) {
      notifyWarning("今天的页数不够", `还剩 ${remaining} 页，这次需要 ${total} 页`);
      return;
    }
    setSubmitting(true);
    try {
      await api<Job>("/api/jobs", {
        method: "POST",
        body: JSON.stringify({
          file_id: file.file_id,
          printer_id: printerId,
          copies,
          duplex,
          color_mode: "monochrome",
          paper_size: "A4",
        }),
      });
      notifySuccess("已发送", "任务已交给打印机，出纸后状态会变成“已出纸”");
      setFile(null);
      setCopies(1);
      if (inputRef.current) inputRef.current.value = "";
      const [r, d] = await Promise.all([
        api<{jobs: Job[]}>("/api/jobs?limit=6"),
        api<{daily_pages: number}>("/api/dashboard"),
      ]);
      setJobs(r.jobs ?? []);
      setUsedPages(d.daily_pages ?? 0);
    } catch (e) {
      const msg = e instanceof Error ? e.message : "提交失败";
      const code = (e as Error & {code?: string}).code;
      if (code === "QUOTA_EXCEEDED") notifyWarning("今天的页数不够", msg);
      else if (code === "HELPER_OFFLINE") notifyWarning("帮忙的电脑不在线", msg);
      else if (code === "PRINTER_OFFLINE") notifyWarning("打印机连不上", msg);
      else notifyError("发送失败", msg);
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <div className="space-y-6">
      <PageHeader
        subtitle="选文件、选打印机、点发送。不用装驱动。"
        title="打印文件"
      />

      {loadError ? (
        <div className="rounded-xl border border-danger/30 bg-danger/8 px-4 py-3 text-sm text-danger">
          加载失败：{loadError}
          <Button className="ml-3" size="sm" variant="ghost" onPress={() => void load()}>
            重试
          </Button>
        </div>
      ) : null}

      {needsSetup ? (
        <div className="rounded-xl border border-warning/30 bg-warning/8 px-4 py-3 text-sm text-warning">
        现在还没有可以出纸的打印机。请联系管理员确认打印机已开机；需要转发时，也确认那台 Windows 电脑开着。
        </div>
      ) : null}

      <div className="grid gap-6 lg:grid-cols-[1.4fr_1fr]">
        <div className="space-y-6">
          <SectionCard description="PDF、Word、Excel、PPT、JPG、PNG 都可以。" title="1. 选择文件">
            <input
              accept=".pdf,.docx,.xlsx,.pptx,.jpg,.jpeg,.png"
              className="hidden"
              onChange={(e) => pick(e.target.files?.[0])}
              ref={inputRef}
              type="file"
            />
            <div
              className={`relative flex min-h-56 cursor-pointer flex-col items-center justify-center gap-3 rounded-2xl border-2 border-dashed px-6 py-10 text-center transition ${
                drag ? "border-accent bg-accent/8" : "border-separator bg-default/20 hover:border-accent/50"
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
                pick(e.dataTransfer.files?.[0]);
              }}
              role="button"
              tabIndex={0}
            >
              {uploading ? (
                <>
                  <SpinnerRing />
                  <div className="text-sm font-medium">正在上传…</div>
                  <div className="text-xs text-muted">大文件或 Office 文档需要几秒钟</div>
                </>
              ) : file ? (
                <>
                  <div className="grid size-14 place-items-center rounded-2xl bg-success/12 text-success">
                    <CheckIcon />
                  </div>
                  <div className="max-w-full truncate text-base font-medium">{file.name}</div>
                  <div className="flex flex-wrap items-center justify-center gap-2 text-xs text-muted">
                    <span>{file.page_count} 页</span>
                    <span>·</span>
                    <span>{fmt.bytes(file.size_bytes)}</span>
                  </div>
                  <Button
                    size="sm"
                    variant="secondary"
                    onPress={(e) => {
                      e.continuePropagation();
                      setFile(null);
                      if (inputRef.current) inputRef.current.value = "";
                    }}
                  >
                    换一个文件
                  </Button>
                </>
              ) : (
                <>
                  <div className="grid size-14 place-items-center rounded-2xl bg-accent/12 text-accent">
                    <UploadIcon />
                  </div>
                  <div className="text-base font-medium">拖入文件，或点击选择</div>
                  <div className="text-xs text-muted">最大 {maxUploadMB} MB · 最多 200 页</div>
                </>
              )}
            </div>
          </SectionCard>

          <SectionCard
            actions={
              <Button size="sm" variant="ghost" onPress={() => void load()}>
                刷新
              </Button>
            }
            description="灰掉的打印机暂时不能用；换一台，或联系管理员。"
            title="2. 选打印机"
          >
            {printers.length === 0 ? (
              <EmptyBox
                action={
                  <Button size="sm" variant="secondary" onPress={() => void load()}>
                    重新加载
                  </Button>
                }
                description="管理员还没把打印机分给你，或者负责转发的那台 Windows 电脑没开机。可以先联系管理员确认。"
                title="现在没有可用的打印机"
              />
            ) : (
              <div className="grid gap-3 sm:grid-cols-2">
                {printers.map((p) => {
                  const ok = !!usable(p);
                  const first = (p.endpoints ?? [])[0];
                  const active = p.id === printerId;
                  return (
                    <button
                      className={`rounded-2xl border p-4 text-left transition ${
                        !ok
                          ? "cursor-not-allowed border-separator/40 bg-default/20 opacity-65"
                          : active
                          ? "border-accent bg-accent/8 shadow-sm"
                          : "border-separator/70 bg-surface/50 hover:border-accent/40"
                      }`}
                      disabled={!ok}
                      key={p.id}
                      onClick={() => setPrinterId(p.id)}
                      type="button"
                    >
                      <div className="flex items-start justify-between gap-2">
                        <div className="min-w-0">
                          <div className="truncate font-medium">{p.display_name}</div>
                          <div className="mt-0.5 truncate text-xs text-muted">
                            {[p.location, p.model].filter(Boolean).join(" · ") || "未填写位置"}
                          </div>
                        </div>
                        <span
                          className={`mt-0.5 grid size-5 shrink-0 place-items-center rounded-full border ${
                            active ? "border-accent bg-accent text-accent-foreground" : "border-separator"
                          }`}
                        >
                          {active ? <CheckIcon size={12} /> : null}
                        </span>
                      </div>
                      <div className="mt-3">
                        {ok ? (
                          <Chip color="success" size="sm" variant="soft">
                            现在可以打印
                          </Chip>
                        ) : first?.kind === "agent" ? (
                          <Chip color="warning" size="sm" variant="soft">
                            帮忙的电脑没开机
                          </Chip>
                        ) : first ? (
                          <Chip color="warning" size="sm" variant="soft">
                            打印机现在连不上
                          </Chip>
                        ) : (
                          <Chip color="warning" size="sm" variant="soft">
                            还没接好
                          </Chip>
                        )}
                      </div>
                    </button>
                  );
                })}
              </div>
            )}
          </SectionCard>
        </div>

        <div className="space-y-6">
          <SectionCard title="3. 确认发送">
            <div className="space-y-4">
              <Field label="打印几份">
                <div className="flex items-center gap-2">
                  <Button isIconOnly size="sm" variant="secondary" onPress={() => setCopies((c) => Math.max(1, c - 1))}>
                    −
                  </Button>
                  <div className="mono grid h-9 w-16 place-items-center rounded-xl border border-separator bg-surface text-sm">
                    {copies}
                  </div>
                  <Button isIconOnly size="sm" variant="secondary" onPress={() => setCopies((c) => Math.min(99, c + 1))}>
                    +
                  </Button>
                  <span className="ml-1 text-xs text-muted">最多 99 份</span>
                </div>
              </Field>
              <Field
                hint={supportsDuplex ? undefined : "这台打印机不支持所选的双面方式，请改回单面。"}
                label="单面还是双面"
              >
                <Select fullWidth selectedKey={duplex} onSelectionChange={(k) => setDuplex(String(k) as typeof duplex)}>
                  <Select.Trigger>
                    <Select.Value />
                    <Select.Indicator />
                  </Select.Trigger>
                  <Select.Popover>
                    <ListBox>
                      <ListBox.Item id="one-sided" textValue="单面">
                        单面
                        <ListBox.ItemIndicator />
                      </ListBox.Item>
                      <ListBox.Item id="two-sided-long-edge" textValue="双面 · 长边翻">
                        双面 · 长边翻（像书一样）
                        <ListBox.ItemIndicator />
                      </ListBox.Item>
                      <ListBox.Item id="two-sided-short-edge" textValue="双面 · 短边翻">
                        双面 · 短边翻
                        <ListBox.ItemIndicator />
                      </ListBox.Item>
                    </ListBox>
                  </Select.Popover>
                </Select>
              </Field>
              <div className="rounded-xl bg-default/50 px-3 py-2.5 text-xs leading-5 text-muted">
                纸张固定 A4、黑白。
                {selected ? (
                  <div className="mt-1">
                    将发送到 <span className="font-medium text-foreground">{selected.display_name}</span>
                  </div>
                ) : null}
              </div>
              <div className="flex items-center justify-between border-t border-separator/60 pt-3">
                <span className="text-sm text-muted">合计</span>
                <span className="mono text-lg font-semibold">{total} 页</span>
              </div>
              <Button
                fullWidth
                isDisabled={!file || !printerId || submitting || !supportsDuplex}
                size="lg"
                onPress={submit}
              >
                {submitting ? "正在发送…" : "发送打印"}
              </Button>
              {file && !endpoint ? (
                <p className="text-center text-xs text-warning">
                  这台打印机现在不能出纸。换成“现在可以打印”的设备，或联系管理员。
                </p>
              ) : null}
              {user.daily_page_quota > 0 ? (
                <p className="text-center text-xs text-muted">
                  今天还剩 {remaining} 页可用。
                </p>
              ) : null}
            </div>
          </SectionCard>

          <SectionCard
            actions={
              <Button size="sm" variant="ghost" onPress={() => (window.location.hash = "/jobs")}>
                全部
              </Button>
            }
            title="最近发送"
          >
            {jobs.length === 0 ? (
              <p className="py-6 text-center text-sm text-muted">还没有发过打印任务。</p>
            ) : (
              <div className="space-y-2">
                {jobs.slice(0, 5).map((j) => {
                  const st = jobStatus[j.status] || {label: j.status, tone: "text-muted"};
                  return (
                    <div className="flex items-center gap-3 rounded-xl border border-separator/50 bg-surface/40 px-3 py-2" key={j.id}>
                      <div className="min-w-0 flex-1">
                        <div className="truncate text-sm font-medium">{j.file_name || "打印文件"}</div>
                        <div className="truncate text-[11px] text-muted">
                          {j.printer_name} · {j.total_pages} 页 · {duplexLabel[j.duplex] || j.duplex}
                        </div>
                      </div>
                      <span className={`shrink-0 text-xs font-medium ${st.tone}`}>{st.label}</span>
                    </div>
                  );
                })}
              </div>
            )}
          </SectionCard>
        </div>
      </div>
    </div>
  );
}

function UploadIcon() {
  return (
    <svg fill="none" height="26" viewBox="0 0 24 24" width="26">
      <path d="M12 16V4m0 0L7.5 8.5M12 4l4.5 4.5M4 15v3a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2v-3" stroke="currentColor" strokeLinecap="round" strokeLinejoin="round" strokeWidth="1.7" />
    </svg>
  );
}
function CheckIcon({size = 20}: {size?: number}) {
  return (
    <svg fill="none" height={size} viewBox="0 0 24 24" width={size}>
      <path d="m5 12.5 4.2 4.2L19 7" stroke="currentColor" strokeLinecap="round" strokeLinejoin="round" strokeWidth="2.2" />
    </svg>
  );
}
function SpinnerRing() {
  return <div className="size-8 animate-spin rounded-full border-2 border-accent/30 border-t-accent" />;
}
