import type { HealthState, SourceRun } from "../api/client";
import { HEALTH_LABEL } from "../pages/admin/adminForm";

/** Health is always spelled out as well as coloured, so it does not rely on colour alone. */
export function HealthBadge({ state, reason }: { state: HealthState; reason?: string }) {
  return <span className={`health health-${state}`} title={reason}>{HEALTH_LABEL[state]}</span>;
}

export function RunStatusBadge({ status }: { status: SourceRun["status"] }) {
  return <span className={`health run-${status}`}>{status === "success" ? "OK" : status === "partial" ? "Partial" : "Failed"}</span>;
}
