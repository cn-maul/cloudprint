export type User = {
  id: string;
  username: string;
  display_name: string;
  role: "user" | "admin";
  daily_page_quota: number;
  is_active: boolean;
  must_change_password: boolean;
};

export type UserGroup = {
  id: string;
  name: string;
  description: string;
  member_count: number;
  printer_ids: string[];
  members?: Array<{
    id: string;
    username: string;
    display_name: string;
    role: string;
    is_active: boolean;
  }>;
  created_at: string;
  updated_at: string;
};

export type Capabilities = {
  document_formats?: string[];
  sides?: string[];
  media?: string[];
  color_modes?: string[];
  max_copies?: number;
  make_model?: string;
  state?: string;
  state_reasons?: string[];
  checked_at?: string;
};

export type Endpoint = {
  id: string;
  printer_id: string;
  kind: "ipp" | "cups";
  uri: string;
  queue_name?: string;
  protocol?: string;
  priority: number;
  is_enabled: boolean;
  health_status: "unknown" | "healthy" | "degraded" | "down";
  last_checked_at?: string;
  last_success_at?: string;
  last_error?: string;
  capabilities: Capabilities;
  capabilities_source: string;
  ppd_name?: string;
  direct_pdf?: boolean;
};

export type Printer = {
  id: string;
  display_name: string;
  location: string;
  vendor: string;
  model: string;
  serial_number: string;
  profile_id: string;
  network_address?: string;
  is_enabled: boolean;
  endpoints: Endpoint[];
  group_ids?: string[];
  created_at: string;
  updated_at: string;
};

export type Job = {
  id: string;
  user_id: string;
  username?: string;
  printer_id: string;
  printer_name?: string;
  endpoint_id?: string;
  endpoint_uri?: string;
  queue_name?: string;
  cups_job_id?: string;
  pdf_file_id?: string;
  file_name?: string;
  status: string;
  copies: number;
  duplex: string;
  color_mode: string;
  paper_size: string;
  page_range?: string;
  page_count: number;
  total_pages: number;
  error_code?: string;
  error_message?: string;
  created_at: string;
  updated_at: string;
  started_at?: string;
  finished_at?: string;
  progress?: {printed: number; total: number};
  retry_of?: string;
};

export type Candidate = {
  id: string;
  source: string;
  discovered_uri: string;
  kind: string;
  host: string;
  port: number;
  path: string;
  mac?: string;
  vendor?: string;
  model?: string;
  serial_number?: string;
  direct: boolean;
  probe_attempted?: boolean;
  status: string;
  possible_ip_change: boolean;
  already_added?: boolean;
  printer_name?: string;
  supported?: boolean;
  unsupported_reason?: string;
  first_seen_at: string;
  last_seen_at: string;
};

export type SupportedModel = {
  id: string;
  name: string;
  vendor?: string;
  driver?: string;
  notes?: string;
};

export type QuickAddResult = {
  printer: Printer;
  direct: boolean;
  mode: "ipp" | "cups";
  message: string;
  queue?: string;
  driver?: string;
};

export type CUPSQueue = {
  name: string;
  state: string;
  enabled: boolean;
  accepting: boolean;
  device_uri?: string;
  make_model?: string;
  jobs: number;
  linked_printer_id?: string;
};

export type CUPSDriver = {
  path: string;
  name: string;
  manufacturer?: string;
  model?: string;
};

export type DriverList = {
  available: boolean;
  reason?: string;
  drivers: CUPSDriver[];
};

export type CUPSStatus = {
  available: boolean;
  reason?: string;
};

export type DiscoveryRun = {
  id: string;
  source: string;
  cidr?: string;
  status: string;
  started_at: string;
  finished_at?: string;
  found_count: number;
  error_message?: string;
};

export type ScanTarget = {
  cidr: string;
  label: string;
};

export type ScanTargets = {
  targets: ScanTarget[];
  client_ip?: string;
  can_scan: boolean;
  configured_cidrs?: string[];
};

export type PrinterStatus = {
  state: "idle" | "processing" | "stopped" | "unknown" | string;
  reasons?: string[];
  message?: string;
  queue?: string;
  queue_state?: string;
  direct_pdf: boolean;
  active_jobs: number;
  model?: string;
};

export type PrinterDetail = {
  printer: Printer;
  status: PrinterStatus;
  jobs: Job[];
  total: number;
  limit: number;
  offset: number;
};

export type PrinterQueueCount = {queued: number; printing: number};

export type PrinterOptions = {
  printer_id: string;
  model: string;
  reachable: boolean;
  paper_sizes: string[];
  copies_max: number;
  can_duplex: boolean;
  can_color: boolean;
  accepts_pdf: boolean;
  sides: string[];
  media: string[];
  operations: number[];
};

export type DashboardData = {
  status_counts?: Record<string, number>;
  printers_enabled: number;
  printers_total: number;
  queued_jobs: number;
  printing_jobs: number;
  failed_jobs_today: number;
  completed_today: number;
  new_candidates: number;
  cups_ready: number;
  cups_total: number;
  daily_pages: number;
  daily_page_quota: number;
  max_upload_mb: number;
  recent_jobs: Job[];
};

export type UploadResult = {
  id: string;
  file_id: string;
  name: string;
  mime: string;
  size_bytes: number;
  page_count: number;
  converted: boolean;
  preview_url: string;
};

type ErrorEnvelope = {error?: {code?: string; message?: string}};

let csrfToken = "";

export function setCSRF(token: string) {
  csrfToken = token || "";
}

const API_BASE = import.meta.env.VITE_API_BASE ?? "";

export async function api<T>(path: string, init: RequestInit = {}): Promise<T> {
  const headers = new Headers(init.headers);
  if (init.body && !(init.body instanceof FormData) && !headers.has("Content-Type")) {
    headers.set("Content-Type", "application/json");
  }
  const method = (init.method || "GET").toUpperCase();
  if (csrfToken && method !== "GET" && method !== "HEAD") {
    headers.set("X-CSRF-Token", csrfToken);
  }
  const res = await fetch(API_BASE + path, {...init, headers, credentials: "same-origin"});
  if (!res.ok) {
    let msg = `请求失败（${res.status}）`;
    let code = "";
    try {
      const data = (await res.json()) as ErrorEnvelope;
      msg = data.error?.message || msg;
      code = data.error?.code || "";
    } catch {
      /* non-JSON error */
    }
    const err = new Error(msg) as Error & {code?: string; status?: number};
    err.code = code;
    err.status = res.status;
    throw err;
  }
  if (res.status === 204) return undefined as T;
  return (await res.json()) as T;
}

export const fmt = {
  date(v?: string) {
    if (!v) return "—";
    const d = new Date(v);
    if (Number.isNaN(d.getTime())) return v;
    return d.toLocaleString("zh-CN", {month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit"});
  },
  bytes(n: number) {
    if (n < 1024) return `${n} B`;
    if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`;
    return `${(n / 1024 / 1024).toFixed(1)} MB`;
  },
};

export const jobStatus: Record<string, {label: string; tone: string}> = {
  uploading: {label: "上传中", tone: "text-muted"},
  converting: {label: "转换中", tone: "text-warning"},
  queued: {label: "等待打印", tone: "text-accent"},
  claimed: {label: "正在发送", tone: "text-accent"},
  printing: {label: "正在打印", tone: "text-warning"},
  completed: {label: "已出纸", tone: "text-success"},
  failed: {label: "失败", tone: "text-danger"},
  canceled: {label: "已取消", tone: "text-muted"},
  rejected: {label: "已拒绝", tone: "text-danger"},
};

export const printerStateLabel: Record<string, string> = {
  idle: "就绪",
  processing: "打印中",
  stopped: "已停止",
  unknown: "未知",
};

export const healthLabel: Record<string, {label: string; color: "success" | "warning" | "danger" | "default"}> = {
  healthy: {label: "正常", color: "success"},
  degraded: {label: "部分可用", color: "warning"},
  down: {label: "连不上", color: "danger"},
  unknown: {label: "待检测", color: "default"},
};

export const duplexLabel: Record<string, string> = {
  "one-sided": "单面",
  "two-sided-long-edge": "双面 · 长边翻",
  "two-sided-short-edge": "双面 · 短边翻",
};

export const endpointKindLabel: Record<string, string> = {
  ipp: "打印机直连",
  cups: "服务器 CUPS 队列",
};
