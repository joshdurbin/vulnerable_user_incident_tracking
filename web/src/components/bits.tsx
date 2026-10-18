import type { ReactNode } from "react";
import { ApiError } from "../api/client";
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
  const problems = error instanceof ApiError && error.problems.length > 1 ? error.problems : [];
  return (
    <div className="error" role="alert">
      <p>{error instanceof Error ? error.message : String(error)}</p>
      {problems.length > 0 && <ul>{problems.map((p) => <li key={p}>{p}</li>)}</ul>}
    </div>
  );
}
