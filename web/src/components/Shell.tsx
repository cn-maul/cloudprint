import {useEffect, useState} from "react";
import {Button} from "@heroui/react";
import type {Session} from "../App";
import type {User} from "../lib/api";
import PrintersPage from "../pages/Printers";
import PrinterDetailPage from "../pages/PrinterDetail";
import AddPrinterPage from "../pages/AddPrinter";
import AdminPrintersPage from "../pages/AdminPrinters";
import {LockIcon, LogoutIcon, PrinterIcon} from "./Icon";

// Accounts, quotas and groups are intentionally not reachable from here yet.
type Route = {view: "list"} | {view: "detail"; id: string} | {view: "add"} | {view: "admin"};

// The shell only renders with a real principal: App shows the login screen
// instead when the server returns no user at all.
type AuthSession = Session & {user: User};

export default function Shell({
  session,
  onRequestLogin,
  onLogout,
}: {
  session: AuthSession;
  onRequestLogin: () => void;
  onLogout: () => Promise<void>;
}) {
  const [route, setRoute] = useState<Route>(readHash());
  // Guests may use printers that already exist; registering and removing one is
  // an admin job (the whole /api/admin tree refuses them). Keeping the route
  // guard here means a stale or hand-typed "#/add" lands on the list instead of
  // a page whose every request answers 403.
  const isAdmin = session.user.role === "admin";
  const view: Route =
    (route.view === "add" || route.view === "admin") && !isAdmin ? {view: "list"} : route;

  useEffect(() => {
    const onHash = () => setRoute(readHash());
    window.addEventListener("hashchange", onHash);
    return () => window.removeEventListener("hashchange", onHash);
  }, []);

  function go(next: Route) {
    window.location.hash =
      next.view === "detail"
        ? `/p/${next.id}`
        : next.view === "add"
          ? "/add"
          : next.view === "admin"
            ? "/admin"
            : "";
    setRoute(next);
  }

  return (
    <div className="app-bg min-h-screen">
      <header className="border-b border-separator/50 bg-background/70 backdrop-blur">
        <div className="mx-auto flex w-full max-w-[1120px] items-center gap-3 px-4 py-3 sm:px-6">
          <div className="flex items-center gap-2.5">
            <div className="grid size-9 place-items-center rounded-xl bg-accent text-accent-foreground">
              <PrinterIcon size={18} />
            </div>
            <span className="text-sm font-semibold tracking-tight">高二语数外办公室</span>
          </div>
          <div className="flex-1" />
          {isAdmin ? (
            <>
              <Button
                size="sm"
                variant={view.view === "admin" ? "secondary" : "ghost"}
                onPress={() => go({view: view.view === "admin" ? "list" : "admin"})}
              >
                {view.view === "admin" ? "返回打印" : "管理"}
              </Button>
              <span className="hidden text-xs text-muted sm:block">{session.user.display_name}</span>
              <Button
                isIconOnly
                aria-label="退出登录"
                size="sm"
                variant="ghost"
                onPress={() => void onLogout()}
              >
                <LogoutIcon size={17} />
              </Button>
            </>
          ) : (
            <Button size="sm" variant="ghost" onPress={onRequestLogin}>
              <LockIcon size={14} />
              管理员登录
            </Button>
          )}
        </div>
      </header>
      <main className="mx-auto w-full max-w-[1120px] px-4 py-6 sm:px-6 lg:py-8">
        {view.view === "detail" ? (
          <PrinterDetailPage
            key={view.id}
            printerId={view.id}
            isAdmin={isAdmin}
            onBack={() => go({view: "list"})}
          />
        ) : view.view === "add" ? (
          <AddPrinterPage
            onAdded={(id) => go({view: "detail", id})}
            onBack={() => go({view: "admin"})}
          />
        ) : view.view === "admin" ? (
          <AdminPrintersPage
            onAdd={() => go({view: "add"})}
            onBack={() => go({view: "list"})}
            onOpen={(id) => go({view: "detail", id})}
          />
        ) : (
          <PrintersPage
            canAdd={isAdmin}
            onAdd={() => go({view: "add"})}
            onOpen={(id) => go({view: "detail", id})}
          />
        )}
      </main>
    </div>
  );
}

function readHash(): Route {
  const m = window.location.hash.match(/^#\/p\/(.+)$/);
  if (m) return {view: "detail", id: decodeURIComponent(m[1])};
  if (window.location.hash === "#/add") return {view: "add"};
  if (window.location.hash === "#/admin") return {view: "admin"};
  return {view: "list"};
}
