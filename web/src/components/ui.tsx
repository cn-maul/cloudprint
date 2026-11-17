import type {ReactNode} from "react";
import {Chip} from "@heroui/react";
import {healthLabel} from "../lib/api";

export function PageHeader({
  title,
  subtitle,
  actions,
}: {
  title: string;
  subtitle?: string;
  actions?: ReactNode;
}) {
  return (
    <div className="flex flex-col gap-4 sm:flex-row sm:items-end sm:justify-between">
      <div className="min-w-0">
        <h1 className="text-2xl font-semibold tracking-tight sm:text-3xl">{title}</h1>
        {subtitle ? <p className="mt-1 text-sm text-muted">{subtitle}</p> : null}
      </div>
      {actions ? <div className="flex shrink-0 flex-wrap items-center gap-2">{actions}</div> : null}
    </div>
  );
}

export function StatCard({
  label,
  value,
  hint,
  accent,
}: {
  label: string;
  value: ReactNode;
  hint?: string;
  accent?: "accent" | "success" | "warning" | "danger";
}) {
  const color =
    accent === "success"
      ? "text-success"
      : accent === "warning"
        ? "text-warning"
        : accent === "danger"
          ? "text-danger"
          : accent === "accent"
            ? "text-accent"
            : "text-foreground";
  return (
    <div className="rounded-2xl border border-separator/60 bg-surface/70 p-4 shadow-sm backdrop-blur">
      <div className="text-xs font-medium uppercase tracking-wide text-muted">{label}</div>
      <div className={`mt-2 text-2xl font-semibold tabular-nums ${color}`}>{value}</div>
      {hint ? <div className="mt-1 text-xs text-muted">{hint}</div> : null}
    </div>
  );
}

export function HealthChip({status}: {status: string}) {
  const h = healthLabel[status] || healthLabel.unknown;
  return (
    <Chip color={h.color} size="sm" variant="soft">
      {h.label}
    </Chip>
  );
}

export function EmptyBox({
  title,
  description,
  action,
}: {
  title: string;
  description: string;
  action?: ReactNode;
}) {
  return (
    <div className="flex flex-col items-center justify-center gap-2 rounded-2xl border border-dashed border-separator bg-surface/40 px-6 py-14 text-center">
      <div className="text-base font-medium">{title}</div>
      <p className="max-w-md text-sm text-muted">{description}</p>
      {action ? <div className="mt-3">{action}</div> : null}
    </div>
  );
}

export function Field({label, hint, children}: {label: string; hint?: string; children: ReactNode}) {
  return (
    <label className="flex flex-col gap-1.5">
      <span className="text-sm font-medium">{label}</span>
      {children}
      {hint ? <span className="text-xs text-muted">{hint}</span> : null}
    </label>
  );
}

export function SectionCard({
  title,
  description,
  actions,
  children,
  className = "",
}: {
  title: string;
  description?: string;
  actions?: ReactNode;
  children: ReactNode;
  className?: string;
}) {
  return (
    <section className={`rounded-2xl border border-separator/60 bg-surface/70 shadow-sm backdrop-blur ${className}`}>
      <header className="flex flex-col gap-2 border-b border-separator/50 px-5 py-4 sm:flex-row sm:items-center sm:justify-between">
        <div>
          <h2 className="text-base font-semibold">{title}</h2>
          {description ? <p className="mt-0.5 text-xs text-muted">{description}</p> : null}
        </div>
        {actions ? <div className="flex flex-wrap items-center gap-2">{actions}</div> : null}
      </header>
      <div className="p-5">{children}</div>
    </section>
  );
}
