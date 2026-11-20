import {useEffect, useState} from "react";
import {Button} from "@heroui/react";
import type {Session} from "../App";
import PrintersPage from "../pages/Printers";
import PrinterDetailPage from "../pages/PrinterDetail";
import {LogoutIcon, PrinterIcon} from "./Icon";

// Two views only: the printer list and one printer's queue. Accounts,
// quotas, groups and discovery are intentionally not reachable from here yet.
type Route = {view: "list"} | {view: "detail"; id: string};

export default function Shell({
  session,
  onLogout,
}: {
  session: Session;
  onLogout: () => Promise<void>;
}) {
  const [route, setRoute] = useState<Route>(readHash());

  useEffect(() => {
    const onHash = () => setRoute(readHash());
    window.addEventListener("hashchange", onHash);
    return () => window.removeEventListener("hashchange", onHash);
  }, []);

  function go(next: Route) {
    window.location.hash = next.view === "detail" ? `/p/${next.id}` : "";
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
          {session.anonymous ? null : (
            <>
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
          )}
        </div>
      </header>
      <main className="mx-auto w-full max-w-[1120px] px-4 py-6 sm:px-6 lg:py-8">
        {route.view === "detail" ? (
          <PrinterDetailPage
            key={route.id}
            printerId={route.id}
            onBack={() => go({view: "list"})}
          />
        ) : (
          <PrintersPage onOpen={(id) => go({view: "detail", id})} />
        )}
      </main>
    </div>
  );
}

function readHash(): Route {
  const m = window.location.hash.match(/^#\/p\/(.+)$/);
  return m ? {view: "detail", id: decodeURIComponent(m[1])} : {view: "list"};
}
