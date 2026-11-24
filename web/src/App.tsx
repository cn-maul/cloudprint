import {useCallback, useEffect, useMemo, useState} from "react";
import {Button, Spinner} from "@heroui/react";
import {api, setCSRF, type User} from "./lib/api";
import Login from "./pages/Login";
import Shell from "./components/Shell";

export type Session = {
  // `user` is null only when the server requires a login: without a session
  // and without guest mode there is no principal to act as.
  user: User | null;
  csrf_token: string;
  // Visitors do not need an account: the server resolves them to the guest
  // principal. `authenticated` is what the shell keys on for admin abilities.
  anonymous?: boolean;
  authenticated?: boolean;
};

export default function App() {
  const [session, setSession] = useState<Session | null>(null);
  const [loading, setLoading] = useState(true);
  const [loginOpen, setLoginOpen] = useState(false);

  const refreshSession = useCallback(async () => {
    try {
      const s = await api<Session>("/api/auth/me");
      setCSRF(s.csrf_token);
      setSession(s);
    } catch {
      setSession(null);
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void refreshSession();
  }, [refreshSession]);

  const content = useMemo(() => {
    if (loading) {
      return (
        <div className="app-bg grid min-h-screen place-items-center">
          <Spinner size="sm" />
        </div>
      );
    }
    if (!session) return <SessionError onRetry={refreshSession} />;
    if (!session.user) {
      return (
        <>
          <div className="app-bg min-h-screen" />
          <Login
            allowGuest={false}
            onClose={() => void refreshSession()}
            onLogin={async (s) => {
              setCSRF(s.csrf_token);
              await refreshSession();
            }}
          />
        </>
      );
    }
    return (
      <Shell
        session={{...session, user: session.user}}
        onRequestLogin={() => setLoginOpen(true)}
        onLogout={async () => {
          try {
            await api("/api/auth/logout", {method: "POST"});
          } finally {
            setCSRF("");
            await refreshSession();
          }
        }}
      />
    );
  }, [loading, session, loginOpen, refreshSession]);

  return (
    <>
      {content}
      {loginOpen ? (
        <Login
          onClose={() => setLoginOpen(false)}
          onLogin={async (s) => {
            setCSRF(s.csrf_token);
            setLoginOpen(false);
            await refreshSession();
          }}
        />
      ) : null}
    </>
  );
}

// SessionError is not "please log in": a guest session is always valid. This
// only shows when the server is unreachable or the guest row is missing.
function SessionError({onRetry}: {onRetry: () => Promise<void>}) {
  const [busy, setBusy] = useState(false);
  return (
    <div className="app-bg grid min-h-screen place-items-center px-4">
      <div className="max-w-sm space-y-3 text-center">
        <p className="text-sm">连不上服务器</p>
        <p className="text-xs text-muted">请确认服务已启动，然后重试。</p>
        <Button
          isDisabled={busy}
          onPress={() => {
            setBusy(true);
            void onRetry().finally(() => setBusy(false));
          }}
          size="sm"
        >
          重试
        </Button>
      </div>
    </div>
  );
}
