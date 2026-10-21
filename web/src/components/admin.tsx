import type { HealthState, Job, SourceRun } from "../api/client";
import { HEALTH_LABEL } from "../pages/admin/adminForm";
import { STATUS_LABEL } from "../pages/admin/jobs";

/** Health is always spelled out as well as coloured, so it does not rely on colour alone. */
export function HealthBadge({ state, reason }: { state: HealthState; reason?: string }) {
  return <span className={`health health-${state}`} title={reason}>{HEALTH_LABEL[state]}</span>;
}

export function RunStatusBadge({ status }: { status: SourceRun["status"] }) {
  return <span className={`health run-${status}`}>{status === "success" ? "OK" : status === "partial" ? "Partial" : "Failed"}</span>;
}

/** A backfill or reprocess run's state. A paused backfill is still "running" to Temporal, so say paused. */
export function JobStatusBadge({ job }: { job: Job }) {
  const paused = job.status === "running" && job.backfill?.phase === "paused";
  return <span className={`health job-${paused ? "paused" : job.status}`}>{paused ? "Paused" : STATUS_LABEL[job.status]}</span>;
}
