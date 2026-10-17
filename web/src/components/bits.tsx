import type { ReactNode } from "react";
import { SEVERITY_COLOR, label } from "../format";

export function SeverityBadge({ severity }: { severity: string }) {
  return (
    <span className="badge" style={{ background: SEVERITY_COLOR[severity] ?? SEVERITY_COLOR.unknown }}>
      {label(severity)}
    </span>
  );
}

export function StatCard({ title, value, hint }: { title: string; value: ReactNode; hint?: string }) {
  return (
    <div className="stat">
      <div className="stat-value">{value}</div>
      <div className="stat-title">{title}</div>
      {hint && <div className="stat-hint">{hint}</div>}
    </div>
  );
}

export function Loading({ what }: { what: string }) {
  return <p className="muted" role="status">Loading {what}…</p>;
}

export function ErrorNote({ error }: { error: unknown }) {
  return <p className="error" role="alert">{error instanceof Error ? error.message : String(error)}</p>;
}
