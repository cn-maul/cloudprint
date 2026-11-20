import {useEffect, useMemo, useState} from "react";
import {Spinner} from "@heroui/react";
import {api, setCSRF, type User} from "./lib/api";
import Login from "./pages/Login";
import Shell from "./components/Shell";

export type Session = {user: User; csrf_token: string; anonymous?: boolean};

export default function App() {
  const [session, setSession] = useState<Session | null>(null);
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    let alive = true;
    api<Session>("/api/auth/me")
      .then((s) => {
        if (!alive) return;
        setCSRF(s.csrf_token);
        setSession(s);
      })
      .catch(() => {
        if (alive) setSession(null);
      })
      .finally(() => {
        if (alive) setLoading(false);
      });
    return () => {
      alive = false;
    };
  }, []);

  const content = useMemo(() => {
    if (loading) {
      return (
        <div className="app-bg grid min-h-screen place-items-center">
          <Spinner size="sm" />
        </div>
      );
    }
    if (!session) {
      return (
        <Login
          onLogin={(s) => {
            setCSRF(s.csrf_token);
            setSession(s);
          }}
        />
      );
    }
    return (
      <Shell
        session={session}
        onLogout={async () => {
          try {
            await api("/api/auth/logout", {method: "POST"});
          } finally {
            setCSRF("");
            setSession(null);
          }
        }}
      />
    );
  }, [loading, session]);

  return content;
}
