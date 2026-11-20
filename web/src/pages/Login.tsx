import {useState} from "react";
import {Button, Input} from "@heroui/react";
import {api, setCSRF, type User} from "../lib/api";
import {AlertIcon, ArrowRightIcon, LockIcon, PrinterIcon, UserIcon} from "../components/Icon";

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
      <form
        className="rise w-full max-w-sm space-y-5 rounded-3xl border border-separator/60 bg-surface/80 p-8 shadow-2xl backdrop-blur"
        onSubmit={submit}
      >
        <div className="mx-auto grid size-14 place-items-center rounded-2xl bg-accent text-accent-foreground shadow-lg shadow-accent/25">
          <PrinterIcon size={26} />
        </div>

        <div className="space-y-2.5">
          <label className="flex items-center gap-2">
            <span className="grid size-9 shrink-0 place-items-center rounded-lg bg-default/50 text-muted">
              <UserIcon size={16} />
            </span>
            <Input
              autoComplete="username"
              autoFocus
              fullWidth
              value={username}
              onChange={(e) => setUsername(e.target.value)}
            />
          </label>
          <label className="flex items-center gap-2">
            <span className="grid size-9 shrink-0 place-items-center rounded-lg bg-default/50 text-muted">
              <LockIcon size={16} />
            </span>
            <Input
              autoComplete="current-password"
              fullWidth
              type="password"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
            />
          </label>
        </div>

        {error ? (
          <div className="flex items-center gap-2 rounded-xl border border-danger/30 bg-danger/10 px-3 py-2 text-xs text-danger">
            <AlertIcon size={14} />
            <span className="min-w-0 flex-1 truncate">{error}</span>
          </div>
        ) : null}

        <Button fullWidth isDisabled={busy || !password} size="lg" type="submit">
          <ArrowRightIcon size={17} />
        </Button>
      </form>
    </div>
  );
}
