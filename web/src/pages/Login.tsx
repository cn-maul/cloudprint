import {useState} from "react";
import {Button, Input} from "@heroui/react";
import {api, setCSRF, type User} from "../lib/api";

export default function Login({onLogin}: {onLogin: (s: {user: User; csrf_token: string}) => void}) {
  const [username, setUsername] = useState("admin");
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError("");
    try {
      const s = await api<{user: User; csrf_token: string}>("/api/auth/login", {
        method: "POST",
        body: JSON.stringify({username, password}),
      });
      setCSRF(s.csrf_token);
      onLogin(s);
    } catch (err) {
      setError(err instanceof Error ? err.message : "登录失败");
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="app-bg grid min-h-screen place-items-center px-4">
      <div className="rise grid w-full max-w-4xl overflow-hidden rounded-3xl border border-separator/60 bg-surface/80 shadow-2xl backdrop-blur md:grid-cols-[1.1fr_1fr]">
        <div className="relative hidden flex-col justify-between gap-10 bg-linear-to-br from-accent/15 via-surface to-surface-secondary p-8 md:flex">
          <div className="flex items-center gap-3">
            <div className="grid size-11 place-items-center rounded-2xl bg-accent text-accent-foreground">
              <PrinterGlyph />
            </div>
            <div>
              <div className="text-lg font-semibold tracking-tight">CloudPrint</div>
              <div className="text-xs text-muted">局域网云打印</div>
            </div>
          </div>
          <div className="space-y-4">
            <h2 className="text-3xl leading-tight font-semibold tracking-tight">
              上传文件，
              <br />
              在任意打印机出纸。
            </h2>
            <p className="max-w-sm text-sm leading-6 text-muted">
              不用在每台电脑上装驱动。打开网页，上传文件，选一台打印机就能出纸。
            </p>
          </div>
          <div className="grid gap-2 text-xs text-muted">
            <Feature text="浏览器上传，不用装驱动" />
            <Feature text="管理员分好组，同事只看自己能用的打印机" />
            <Feature text="打印进度实时可见" />
          </div>
        </div>
        <form className="flex flex-col justify-center gap-5 p-8 sm:p-10" onSubmit={submit}>
          <div className="md:hidden">
            <div className="text-xl font-semibold">CloudPrint</div>
            <div className="text-xs text-muted">局域网云打印平台</div>
          </div>
          <div>
            <h1 className="text-xl font-semibold">登录</h1>
            <p className="mt-1 text-sm text-muted">使用管理员分配的账号。</p>
          </div>
          <label className="flex flex-col gap-1.5">
            <span className="text-sm font-medium">用户名</span>
            <Input
              autoComplete="username"
              autoFocus
              fullWidth
              value={username}
              onChange={(e) => setUsername(e.target.value)}
            />
          </label>
          <label className="flex flex-col gap-1.5">
            <span className="text-sm font-medium">密码</span>
            <Input
              autoComplete="current-password"
              fullWidth
              type="password"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
            />
          </label>
          {error ? (
            <div className="rounded-xl border border-danger/30 bg-danger/10 px-3 py-2 text-sm text-danger">{error}</div>
          ) : null}
          <Button fullWidth isDisabled={busy || !password} size="lg" type="submit">
            {busy ? "登录中…" : "进入控制台"}
          </Button>
          <p className="text-center text-xs text-muted">
            首次启动时生成的初始密码只显示在服务器控制台一次。
          </p>
        </form>
      </div>
    </div>
  );
}

function Feature({text}: {text: string}) {
  return (
    <div className="flex items-center gap-2">
      <span className="size-1.5 rounded-full bg-accent" />
      {text}
    </div>
  );
}

function PrinterGlyph() {
  return (
    <svg fill="none" height="22" viewBox="0 0 24 24" width="22">
      <path
        d="M7 8V4.5A1.5 1.5 0 0 1 8.5 3h7A1.5 1.5 0 0 1 17 4.5V8M7 17H5.5A1.5 1.5 0 0 1 4 15.5v-6A1.5 1.5 0 0 1 5.5 8h13A1.5 1.5 0 0 1 20 9.5v6a1.5 1.5 0 0 1-1.5 1.5H17M7 13h10v7.5a.5.5 0 0 1-.5.5h-9a.5.5 0 0 1-.5-.5V13Z"
        stroke="currentColor"
        strokeLinecap="round"
        strokeLinejoin="round"
        strokeWidth="1.7"
      />
    </svg>
  );
}
