import {useCallback, useEffect, useState} from "react";
import {Button, Chip, ListBox, Select, Table} from "@heroui/react";
import {api, duplexLabel, fmt, jobStatus, type Job, type User} from "../lib/api";
import {notifyError, notifySuccess} from "../lib/toast";
import {EmptyBox, PageHeader} from "../components/ui";

const filters = [
  {id: "", label: "全部"},
  {id: "queued,claimed,printing", label: "正在打印"},
  {id: "completed", label: "已出纸"},
  {id: "failed", label: "失败"},
  {id: "canceled", label: "已取消"},
];

export default function JobsPage({user}: {user: User}) {
  const [jobs, setJobs] = useState<Job[]>([]);
  const [total, setTotal] = useState(0);
  const [status, setStatus] = useState("");
  const [loading, setLoading] = useState(true);

  const load = useCallback(async () => {
    setLoading(true);
    try {
      const qs = new URLSearchParams({limit: "100"});
      if (status) qs.set("status", status);
      if (user.role === "admin") qs.set("scope", "all");
      const res = await api<{jobs: Job[]; total: number}>(`/api/jobs?${qs.toString()}`);
      setJobs(res.jobs);
      setTotal(res.total);
    } catch (e) {
      notifyError("载入任务失败", e instanceof Error ? e.message : "");
    } finally {
      setLoading(false);
    }
  }, [status, user.role]);

  useEffect(() => {
    void load();
    const es = new EventSource("/api/jobs/events");
    const refresh = () => void load();
    ["job.created", "job.completed", "job.failed", "job.printing", "job.canceled", "job.claimed"].forEach((ev) =>
      es.addEventListener(ev, refresh),
    );
    return () => es.close();
  }, [load]);

  async function cancel(j: Job) {
    try {
      await api(`/api/jobs/${j.id}/cancel`, {method: "POST"});
      notifySuccess("已取消", j.file_name || j.id);
      void load();
    } catch (e) {
      notifyError("取消失败", e instanceof Error ? e.message : "");
    }
  }

  return (
    <div className="space-y-5">
      <PageHeader
        actions={
          <Select className="w-40" selectedKey={status} onSelectionChange={(k) => setStatus(String(k))}>
            <Select.Trigger>
              <Select.Value />
              <Select.Indicator />
            </Select.Trigger>
            <Select.Popover>
              <ListBox>
                {filters.map((f) => (
                  <ListBox.Item id={f.id} key={f.id || "all"} textValue={f.label}>
                    {f.label}
                    <ListBox.ItemIndicator />
                  </ListBox.Item>
                ))}
              </ListBox>
            </Select.Popover>
          </Select>
        }
        subtitle={
          user.role === "admin"
            ? `所有同事共 ${total} 条记录`
            : `你一共发送过 ${total} 次`
        }
        title={user.role === "admin" ? "全部任务" : "我的任务"}
      />

      {jobs.length === 0 && !loading ? (
        <EmptyBox description="在“打印文件”里发送一次，这里会显示进度。" title="没有记录" />
      ) : (
        <div className="overflow-hidden rounded-2xl border border-separator/60 bg-surface/70 shadow-sm">
          <Table>
            <Table.ScrollContainer>
              <Table.Content aria-label="打印任务列表" className="min-w-[880px]">
                <Table.Header>
                  <Table.Column isRowHeader>文件</Table.Column>
                  <Table.Column>打印机</Table.Column>
                  {user.role === "admin" ? <Table.Column>用户</Table.Column> : null}
                  <Table.Column>页数</Table.Column>
                  <Table.Column>参数</Table.Column>
                  <Table.Column>状态</Table.Column>
                  <Table.Column>时间</Table.Column>
                  <Table.Column>操作</Table.Column>
                </Table.Header>
                <Table.Body>
                  {jobs.map((j) => {
                    const st = jobStatus[j.status] || {label: j.status, tone: "text-muted"};
                    return (
                      <Table.Row id={j.id} key={j.id}>
                        <Table.Cell>
                          <div className="max-w-[220px] truncate font-medium">{j.file_name || "打印文件"}</div>
                          <div className="mono text-[11px] text-muted">{j.id.slice(-10)}</div>
                        </Table.Cell>
                        <Table.Cell>
                          <div className="max-w-[160px] truncate">{j.printer_name || "—"}</div>
                        </Table.Cell>
                        {user.role === "admin" ? <Table.Cell>{j.username || "—"}</Table.Cell> : null}
                        <Table.Cell>
                          <span className="mono">{j.total_pages}</span>
                        </Table.Cell>
                        <Table.Cell>
                          <div className="text-xs">{duplexLabel[j.duplex] || j.duplex}</div>
                          <div className="text-[11px] text-muted">
                            {j.paper_size} · {j.copies} 份
                          </div>
                        </Table.Cell>
                        <Table.Cell>
                          <Chip
                            color={
                              j.status === "completed"
                                ? "success"
                                : j.status === "failed"
                                  ? "danger"
                                  : j.status === "printing" || j.status === "claimed"
                                    ? "warning"
                                    : j.status === "queued"
                                      ? "accent"
                                      : "default"
                            }
                            size="sm"
                            variant="soft"
                          >
                            {st.label}
                          </Chip>
                          {j.error_message ? (
                            <div className="mt-1 max-w-[220px] truncate text-[11px] text-danger" title={j.error_message}>
                              {j.error_message}
                            </div>
                          ) : null}
                        </Table.Cell>
                        <Table.Cell>
                          <span className="text-xs text-muted">{fmt.date(j.created_at)}</span>
                        </Table.Cell>
                        <Table.Cell>
                          {j.status === "queued" ? (
                            <Button size="sm" variant="ghost" onPress={() => cancel(j)}>
                              取消
                            </Button>
                          ) : (
                            <span className="text-xs text-muted">—</span>
                          )}
                        </Table.Cell>
                      </Table.Row>
                    );
                  })}
                </Table.Body>
              </Table.Content>
            </Table.ScrollContainer>
          </Table>
        </div>
      )}
    </div>
  );
}
