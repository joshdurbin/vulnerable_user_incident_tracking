import type { BackfillRequest, Job } from "../../api/client";

const PHASE_LABEL: Record<string, string> = {
  running: "Running", paused: "Paused", cancelled: "Cancelled", limit_reached: "Stopped at the article limit", done: "Done",
};

/** One line saying how far a run has got. Empty progress (the worker could not be asked) says so. */
export function progressText(j: Job): string {
  if (j.backfill) {
    const b = j.backfill;
    const parts = [`${b.steps_done} of ${b.total_steps} windows`, `${b.articles_started} of ${b.max_articles} articles started`, `${b.found} found`];
    return parts.join(" · ");
  }
  if (j.reprocess) {
    const r = j.reprocess;
    return `${r.examined} examined · ${r.changed} changed · ${r.failed} failed`;
  }
  return j.status === "running" ? "progress unavailable (is a worker running?)" : "";
}

/** What a backfill is doing right now, in words; a finished or non-backfill run has none. */
export function phaseText(j: Job): string {
  if (!j.backfill) return "";
  return j.status === "running" && j.backfill.current && j.backfill.phase === "running"
    ? `${PHASE_LABEL.running}: ${j.backfill.current}`
    : (PHASE_LABEL[j.backfill.phase] ?? j.backfill.phase);
}

export const STATUS_LABEL: Record<Job["status"], string> = {
  running: "Running", completed: "Finished", failed: "Failed", cancelled: "Cancelled", timed_out: "Timed out", terminated: "Stopped",
};

/** Which buttons a run offers. A reprocess run has no pause, and nothing can be done to a finished run. */
export function controlsFor(j: Job): ("pause" | "resume" | "cancel")[] {
  if (j.status !== "running") return [];
  if (j.kind === "reprocess") return ["cancel"];
  return j.backfill?.phase === "paused" ? ["resume", "cancel"] : ["pause", "cancel"];
}

export interface BackfillForm {
  sources: string[];
  from: string;
  to: string;
  windowHours: string;
  maxArticles: string;
}

export const emptyBackfill = (): BackfillForm => ({ sources: [], from: "", to: "", windowHours: "24", maxArticles: "1000" });

/** What is wrong with the form, found before asking the server. */
export function backfillProblems(f: BackfillForm): string[] {
  const out: string[] = [];
  if (f.sources.length === 0) out.push("Choose at least one source.");
  if (!f.from || !f.to) out.push("Choose a start and an end date.");
  else if (f.to <= f.from) out.push("The end date must be after the start date.");
  const w = Number(f.windowHours);
  if (!Number.isInteger(w) || w < 1 || w > 720) out.push("The window must be 1 to 720 hours.");
  const m = Number(f.maxArticles);
  if (!Number.isInteger(m) || m < 1 || m > 100000) out.push("The article limit must be 1 to 100000.");
  return out;
}

export function backfillRequest(f: BackfillForm): BackfillRequest {
  return { sources: f.sources, from: f.from, to: f.to, window_hours: Number(f.windowHours), max_articles: Number(f.maxArticles) };
}

export interface ReprocessForm { limit: string; batchSize: string; concurrency: string }
export const emptyReprocess = (): ReprocessForm => ({ limit: "100", batchSize: "20", concurrency: "4" });

export function reprocessProblems(f: ReprocessForm): string[] {
  const out: string[] = [];
  const within = (v: string, lo: number, hi: number) => { const n = Number(v); return Number.isInteger(n) && n >= lo && n <= hi; };
  if (!within(f.limit, 0, 1_000_000)) out.push("The limit must be a whole number from 0 (all) to 1000000.");
  if (!within(f.batchSize, 1, 100)) out.push("The batch size must be 1 to 100.");
  if (!within(f.concurrency, 1, 16)) out.push("Concurrency must be 1 to 16.");
  return out;
}
