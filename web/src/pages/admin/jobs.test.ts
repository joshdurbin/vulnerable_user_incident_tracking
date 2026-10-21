import type { Job } from "../../api/client";
import { backfillProblems, controlsFor, emptyBackfill, emptyReprocess, phaseText, progressText, reprocessProblems } from "./jobs";

const base = { id: "backfill-1", kind: "backfill", status: "running", started_at: "2026-10-18T10:00:00Z" } as const;
const backfill = (phase: "running" | "paused" | "done", over: Partial<Job> = {}): Job => ({
  ...base,
  backfill: { phase, steps_done: 2, total_steps: 7, found: 30, articles_started: 12, max_articles: 100, current: "alpha 2026-01-03..2026-01-04" },
  ...over,
});

describe("progressText and phaseText", () => {
  it("says how far a backfill has got and what it is doing", () => {
    expect(progressText(backfill("running"))).toBe("2 of 7 windows · 12 of 100 articles started · 30 found");
    expect(phaseText(backfill("running"))).toBe("Running: alpha 2026-01-03..2026-01-04");
    expect(phaseText(backfill("paused"))).toBe("Paused");
    expect(phaseText(backfill("done", { status: "completed" }))).toBe("Done");
  });
  it("summarises a reprocess run", () => {
    const j: Job = { id: "reprocess-1", kind: "reprocess", status: "completed", started_at: base.started_at, reprocess: { examined: 9, changed: 2, failed: 1 } };
    expect(progressText(j)).toBe("9 examined · 2 changed · 1 failed");
    expect(phaseText(j)).toBe("");
  });
  it("blames the worker when a running job reports nothing", () => {
    expect(progressText({ ...base })).toMatch(/is a worker running/);
    expect(progressText({ ...base, status: "failed" })).toBe("");
  });
});

describe("controlsFor", () => {
  it("offers pause and cancel to a running backfill, resume when paused", () => {
    expect(controlsFor(backfill("running"))).toEqual(["pause", "cancel"]);
    expect(controlsFor(backfill("paused"))).toEqual(["resume", "cancel"]);
  });
  it("offers only cancel to a reprocess run, and nothing once finished", () => {
    expect(controlsFor({ id: "reprocess-1", kind: "reprocess", status: "running", started_at: base.started_at })).toEqual(["cancel"]);
    expect(controlsFor(backfill("done", { status: "completed" }))).toEqual([]);
    expect(controlsFor({ ...base, status: "cancelled" })).toEqual([]);
  });
});

describe("form checks", () => {
  const ok = { ...emptyBackfill(), sources: ["a"], from: "2026-01-01", to: "2026-01-08" };
  it("accepts a complete backfill", () => expect(backfillProblems(ok)).toEqual([]));
  it.each([
    [{ sources: [] }, /at least one source/],
    [{ from: "" }, /start and an end/],
    [{ to: "2026-01-01" }, /after the start/],
    [{ windowHours: "0" }, /1 to 720/],
    [{ windowHours: "1.5" }, /1 to 720/],
    [{ maxArticles: "0" }, /1 to 100000/],
    [{ maxArticles: "lots" }, /1 to 100000/],
  ])("rejects %j", (over, message) => {
    expect(backfillProblems({ ...ok, ...over }).join(" ")).toMatch(message);
  });
  it("checks reprocess bounds, allowing 0 for all", () => {
    expect(reprocessProblems(emptyReprocess())).toEqual([]);
    expect(reprocessProblems({ ...emptyReprocess(), limit: "0" })).toEqual([]);
    expect(reprocessProblems({ ...emptyReprocess(), limit: "-1" })).toHaveLength(1);
    expect(reprocessProblems({ limit: "5", batchSize: "101", concurrency: "17" })).toHaveLength(2);
  });
});
