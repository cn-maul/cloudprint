import {useEffect, useState} from "react";
import {Button, Chip} from "@heroui/react";
import type {Session} from "../App";
import type {User} from "../lib/api";
import PrintPage from "../pages/Print";
import JobsPage from "../pages/Jobs";
import PrinterAdminPage from "../pages/PrinterAdmin";
import AccessPage from "../pages/Access";
import SettingsPage from "../pages/Settings";

export type RouteKey = "print" | "jobs" | "printers" | "access" | "settings";

const NAV: {key: RouteKey; label: string; icon: React.ReactNode; adminOnly?: boolean}[] = [
  {key: "print", label: "打印文件", icon: <IconUpload />},
  {key: "jobs", label: "我的任务", icon: <IconList />},
  {key: "printers", label: "打印机管理", icon: <IconPrinter />, adminOnly: true},
  {key: "access", label: "成员与权限", icon: <IconUsers />, adminOnly: true},
  {key: "settings", label: "我的账号", icon: <IconUser />},
];

export default function Shell({
  session,
  onLogout,
  onUserUpdate,
}: {
  session: Session;
  onLogout: () => Promise<void>;
  onUserUpdate: (u: User) => void;
}) {
  const [route, setRoute] = useState<RouteKey>(readHash());
  const [mobileOpen, setMobileOpen] = useState(false);

  useEffect(() => {
    const onHash = () => setRoute(readHash());
    window.addEventListener("hashchange", onHash);
    return () => window.removeEventListener("hashchange", onHash);
  }, []);

  useEffect(() => {
    const allowed = NAV.find((n) => n.key === route);
    if (allowed?.adminOnly && session.user.role !== "admin") {
      navigate("print");
    }
  }, [route, session.user.role]);

  function navigate(k: RouteKey) {
    window.location.hash = k === "print" ? "" : `#/${k}`;
    setRoute(k);
    setMobileOpen(false);
  }

  const nav = NAV.filter((n) => !n.adminOnly || session.user.role === "admin");
  const page = (() => {
    switch (route) {
      case "jobs":
        return <JobsPage user={session.user} />;
      case "printers":
        return <PrinterAdminPage />;
      case "access":
        return <AccessPage user={session.user} />;
      case "settings":
        return <SettingsPage user={session.user} onUserUpdate={onUserUpdate} onLogout={onLogout} />;
      default:
        return <PrintPage user={session.user} />;
    }
  })();

  return (
    <div className="app-bg min-h-screen lg:flex">
      <aside
        className={`fixed inset-y-0 left-0 z-40 flex w-64 flex-col border-r border-separator/60 bg-surface/90 backdrop-blur transition-transform lg:static lg:translate-x-0 ${
          mobileOpen ? "translate-x-0" : "-translate-x-full"
        }`}
      >
        <div className="flex items-center gap-3 px-5 py-5">
          <div className="grid size-10 place-items-center rounded-xl bg-accent text-accent-foreground">
            <IconPrinter />
          </div>
          <div className="min-w-0">
            <div className="truncate text-base font-semibold tracking-tight">CloudPrint</div>
            <div className="text-[11px] text-muted">
              {session.user.role === "admin" ? "云打印管理" : "局域网云打印"}
            </div>
          </div>
        </div>
        <nav className="flex flex-1 flex-col gap-1 px-3">
          {nav.map((item) => {
            const active = item.key === route;
            return (
              <button
                className={`flex items-center gap-3 rounded-xl px-3 py-2.5 text-sm transition ${
                  active
                    ? "bg-accent/12 font-medium text-accent"
                    : "text-muted hover:bg-default/60 hover:text-foreground"
                }`}
                key={item.key}
                onClick={() => navigate(item.key)}
                type="button"
              >
                <span className="grid size-5 place-items-center">{item.icon}</span>
                {item.label}
              </button>
            );
          })}
        </nav>
        <div className="border-t border-separator/60 p-3">
          <div className="flex items-center gap-3 rounded-xl px-2 py-2">
            <div className="grid size-9 place-items-center rounded-full bg-accent/15 text-sm font-semibold text-accent">
              {session.user.display_name.slice(0, 1).toUpperCase()}
            </div>
            <div className="min-w-0 flex-1">
              <div className="truncate text-sm font-medium">{session.user.display_name}</div>
              <div className="text-[11px] text-muted">
                {session.user.role === "admin"
                  ? "管理员"
                  : session.user.daily_page_quota > 0
                    ? `今日还可打印 ${session.user.daily_page_quota} 页`
                    : "不限页数"}
              </div>
            </div>
            {session.user.role === "admin" ? (
              <Chip color="accent" size="sm" variant="soft">
                Admin
              </Chip>
            ) : null}
          </div>
        </div>
      </aside>
      {mobileOpen ? (
        <button
          aria-label="关闭导航"
          className="fixed inset-0 z-30 bg-black/30 lg:hidden"
          onClick={() => setMobileOpen(false)}
          type="button"
        />
      ) : null}
      <div className="min-w-0 flex-1">
        <header className="sticky top-0 z-20 flex items-center gap-3 border-b border-separator/50 bg-background/75 px-4 py-3 backdrop-blur lg:hidden">
          <Button isIconOnly size="sm" variant="ghost" onPress={() => setMobileOpen(true)}>
            <IconMenu />
          </Button>
          <span className="text-sm font-medium">CloudPrint</span>
        </header>
        <main className="mx-auto w-full max-w-[1200px] px-4 py-6 sm:px-6 lg:py-8">{page}</main>
      </div>
    </div>
  );
}

function readHash(): RouteKey {
  const raw = window.location.hash.replace(/^#\/?/, "");
  const allowed: RouteKey[] = ["print", "jobs", "printers", "access", "settings"];
  return (allowed as string[]).includes(raw) ? (raw as RouteKey) : "print";
}

function IconPrinter() {
  return (
    <svg fill="none" height="18" viewBox="0 0 24 24" width="18">
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
function IconUpload() {
  return (
    <svg fill="none" height="18" viewBox="0 0 24 24" width="18">
      <path d="M12 16V4m0 0L7.5 8.5M12 4l4.5 4.5M4 15v3a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2v-3" stroke="currentColor" strokeLinecap="round" strokeLinejoin="round" strokeWidth="1.7" />
    </svg>
  );
}
function IconList() {
  return (
    <svg fill="none" height="18" viewBox="0 0 24 24" width="18">
      <path d="M8 6h12M8 12h12M8 18h12M4 6h.01M4 12h.01M4 18h.01" stroke="currentColor" strokeLinecap="round" strokeWidth="1.9" />
    </svg>
  );
}
function IconUsers() {
  return (
    <svg fill="none" height="18" viewBox="0 0 24 24" width="18">
      <circle cx="9" cy="8" r="3.2" stroke="currentColor" strokeWidth="1.7" />
      <path d="M2.8 19.2c.7-3.2 3.2-4.9 6.2-4.9s5.5 1.7 6.2 4.9M16 5.4a3 3 0 0 1 0 5.6M18 14.6c2 .5 3.3 2 3.7 4.2" stroke="currentColor" strokeLinecap="round" strokeWidth="1.7" />
    </svg>
  );
}
function IconUser() {
  return (
    <svg fill="none" height="18" viewBox="0 0 24 24" width="18">
      <circle cx="12" cy="8" r="3.5" stroke="currentColor" strokeWidth="1.7" />
      <path d="M4.5 20c.8-3.6 3.8-5.5 7.5-5.5s6.7 1.9 7.5 5.5" stroke="currentColor" strokeLinecap="round" strokeWidth="1.7" />
    </svg>
  );
}
function IconMenu() {
  return (
    <svg fill="none" height="18" viewBox="0 0 24 24" width="18">
      <path d="M4 7h16M4 12h16M4 17h16" stroke="currentColor" strokeLinecap="round" strokeWidth="1.8" />
    </svg>
  );
}
