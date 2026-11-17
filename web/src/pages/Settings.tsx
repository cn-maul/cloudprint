import {useState} from "react";
import {Button, Chip, Input} from "@heroui/react";
import type {User} from "../lib/api";
import {api} from "../lib/api";
import {notifyError, notifySuccess} from "../lib/toast";
import {Field, PageHeader, SectionCard} from "../components/ui";

export default function SettingsPage({
  user,
  onUserUpdate,
  onLogout,
}: {
  user: User;
  onUserUpdate: (u: User) => void;
  onLogout: () => Promise<void>;
}) {
  const [current, setCurrent] = useState("");
  const [next, setNext] = useState("");
  const [confirm, setConfirm] = useState("");
  const [busy, setBusy] = useState(false);
  const [display, setDisplay] = useState(user.display_name);
  const [savingName, setSavingName] = useState(false);

  async function changePassword(e: React.FormEvent) {
    e.preventDefault();
    if (next !== confirm) {
      notifyError("两次输入不一致");
      return;
    }
    setBusy(true);
    try {
      await api("/api/auth/change-password", {
        method: "POST",
        body: JSON.stringify({current_password: current, new_password: next}),
      });
      notifySuccess("密码已更新", "请重新登录");
      await onLogout();
    } catch (e) {
      notifyError("修改失败", e instanceof Error ? e.message : "");
    } finally {
      setBusy(false);
    }
  }

  async function saveName() {
    setSavingName(true);
    try {
      const u = await api<User>("/api/auth/profile", {
        method: "PATCH",
        body: JSON.stringify({display_name: display}),
      });
      onUserUpdate({...user, display_name: u.display_name});
      notifySuccess("已保存");
    } catch (e) {
      notifyError("保存失败", e instanceof Error ? e.message : "");
    } finally {
      setSavingName(false);
    }
  }

  return (
    <div className="space-y-6">
      <PageHeader subtitle="名字、密码和今天还能打多少页。" title="我的账号" />
      <div className="grid gap-6 md:grid-cols-2">
        <SectionCard title="账号信息">
          <div className="space-y-4">
            <div className="flex items-center gap-4">
              <div className="grid size-14 place-items-center rounded-2xl bg-accent/12 text-xl font-semibold text-accent">
                {user.display_name.slice(0, 1).toUpperCase()}
              </div>
              <div>
                <div className="font-medium">{user.display_name}</div>
                <div className="text-xs text-muted">@{user.username}</div>
              </div>
            </div>
            <div className="grid grid-cols-2 gap-3 text-sm">
              <Info label="角色" value={user.role === "admin" ? "管理员" : "普通用户"} />
              <Info label="每日额度" value={user.daily_page_quota > 0 ? `${user.daily_page_quota} 页` : "不限"} />
              <Info label="能用的打印机" value="由管理员按分组分配" />
            </div>
            <div className="flex items-end gap-2 border-t border-separator/60 pt-4">
              <div className="flex-1">
                <Field label="显示名">
                  <Input fullWidth value={display} onChange={(e) => setDisplay(e.target.value)} />
                </Field>
              </div>
              <Button isDisabled={savingName || !display.trim()} variant="secondary" onPress={saveName}>
                保存
              </Button>
            </div>
          </div>
        </SectionCard>
        <SectionCard description="修改后当前会话会退出。" title="修改密码">
          <form className="space-y-3" onSubmit={changePassword}>
            <Field label="当前密码">
              <Input fullWidth type="password" value={current} onChange={(e) => setCurrent(e.target.value)} />
            </Field>
            <Field label="新密码" hint="至少 8 个字符">
              <Input fullWidth type="password" value={next} onChange={(e) => setNext(e.target.value)} />
            </Field>
            <Field label="确认新密码">
              <Input fullWidth type="password" value={confirm} onChange={(e) => setConfirm(e.target.value)} />
            </Field>
            <Button isDisabled={busy || !current || next.length < 8} type="submit" variant="secondary">
              更新密码
            </Button>
          </form>
        </SectionCard>
      </div>
      <SectionCard title="退出登录">
        <div className="flex items-center justify-between gap-4">
          <p className="text-sm text-muted">在共用电脑上使用后，请退出会话。</p>
          <Button variant="danger-soft" onPress={onLogout}>
            退出登录
          </Button>
        </div>
      </SectionCard>
      {user.role !== "admin" ? (
        <div className="flex items-center gap-2 text-xs text-muted">
          <Chip size="sm" variant="soft">
            提示
          </Chip>
          需要新增打印机或审批发现结果，请联系管理员。
        </div>
      ) : null}
    </div>
  );
}

function Info({label, value}: {label: string; value: string}) {
  return (
    <div className="rounded-xl border border-separator/60 bg-default/30 px-3 py-2">
      <div className="text-[11px] text-muted">{label}</div>
      <div className="mt-0.5 font-medium">{value}</div>
    </div>
  );
}
