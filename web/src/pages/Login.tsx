import {useState} from "react";
import {Button, Input} from "@heroui/react";
import {api, setCSRF, type User} from "../lib/api";
import {AlertIcon, ArrowRightIcon, LockIcon, PrinterIcon, UserIcon} from "../components/Icon";

// Login is a modal, not a wall: the shell is usable as a guest and only the
// admin actions ask for credentials.
export default function Login({
  onLogin,
  onClose,
  allowGuest = true,
}: {
  onLogin: (s: {user: User; csrf_token: string}) => void | Promise<void>;
  onClose: () => void;
  // Only guest-enabled deployments have a page to fall back to; when every
  // request requires a login, offering "以游客身份继续" would just re-open this
  // same dialog.
  allowGuest?: boolean;
}) {
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
      await onLogin(s);
    } catch (err) {
      setError(err instanceof Error ? err.message : "登录失败");
    } finally {
      setBusy(false);
    }
  }

  return (
    <div
      className="fixed inset-0 z-50 grid place-items-center bg-black/40 px-4 backdrop-blur-sm"
      onMouseDown={(e) => {
        if (e.target === e.currentTarget) onClose();
      }}
    >
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

        {allowGuest ? (
          <button
            className="-mt-2 w-full text-center text-xs text-muted transition hover:text-foreground"
            onClick={onClose}
            type="button"
          >
            以游客身份继续
          </button>
        ) : null}
      </form>
    </div>
  );
}
