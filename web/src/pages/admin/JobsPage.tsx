import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { api, authHeaders, unwrap, unwrapEmpty, type Job } from "../../api/client";
import { JobStatusBadge } from "../../components/admin";
import { ErrorNote, Loading } from "../../components/bits";
import { ago } from "./adminForm";
import {
  backfillProblems, backfillRequest, controlsFor, emptyBackfill, emptyReprocess, phaseText, progressText, reprocessProblems,
  type BackfillForm, type ReprocessForm,
} from "./jobs";

type Kind = "backfill" | "reprocess";

/** Backfills and reprocess runs: start one, watch it, pause or stop it. */
export default function JobsPage() {
  return (
    <>
      <p className="muted">
        Bulk work that runs in the background. Both are bounded by an article limit so a mistake cannot become an unbounded
        number of model calls, and both are safe to repeat: articles already stored are not fetched or classified twice.
      </p>
      <BackfillSection />
      <ReprocessSection />
    </>
  );
}

function useJobs(kind: Kind) {
  return useQuery({
    queryKey: ["admin", "jobs", kind],
    queryFn: async () => unwrap(await api.GET("/admin/jobs", { params: { query: { kind, limit: 10 } }, headers: authHeaders() })),
    // Poll quickly while something is running, slowly otherwise.
    refetchInterval: (q) => (q.state.data?.some((j) => j.status === "running") ? 4_000 : 30_000),
  });
}

function useControl(kind: Kind) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (v: { id: string; action: "pause" | "resume" | "cancel" }) =>
      unwrapEmpty(await api.POST("/admin/jobs/{id}/control", { params: { path: { id: v.id } }, body: { action: v.action }, headers: authHeaders() })),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["admin", "jobs", kind] }),
  });
}

function BackfillSection() {
  const qc = useQueryClient();
  const [form, setForm] = useState<BackfillForm>(emptyBackfill());
  const sources = useQuery({
    queryKey: ["admin", "sources"],
    queryFn: async () => unwrap(await api.GET("/admin/sources", { params: { query: { days: 30 } }, headers: authHeaders() })),
  });
  const jobs = useJobs("backfill");
  const control = useControl("backfill");
  const start = useMutation({
    mutationFn: async () => unwrap(await api.POST("/admin/backfills", { body: backfillRequest(form), headers: authHeaders() })),
    onSuccess: () => { qc.invalidateQueries({ queryKey: ["admin", "jobs", "backfill"] }); setForm(emptyBackfill()); },
  });
  const problems = backfillProblems(form);
  const set = <K extends keyof BackfillForm>(k: K, v: BackfillForm[K]) => setForm((f) => ({ ...f, [k]: v }));
  const toggle = (slug: string, on: boolean) => set("sources", on ? [...form.sources, slug] : form.sources.filter((s) => s !== slug));

  return (
    <section aria-labelledby="backfills">
      <h2 id="backfills">Backfills</h2>
      <p className="muted">Discover articles from past dates, window by window, then fetch and classify the new ones. Useful for a newly added source or a gap after an outage.</p>
      <JobTable kind="backfill" jobs={jobs} control={control} />
      <form className="card" aria-label="Start a backfill" onSubmit={(e) => { e.preventDefault(); if (problems.length === 0) start.mutate(); }}>
        <h3>Start a backfill</h3>
        <fieldset>
          <legend>Sources</legend>
          {sources.isPending && <Loading what="sources" />}
          {sources.data?.length === 0 && <p className="muted">No sources are configured yet.</p>}
          {sources.data?.map((s) => (
            <label key={s.slug} className="check inline">
              <input type="checkbox" checked={form.sources.includes(s.slug)} onChange={(e) => toggle(s.slug, e.target.checked)} />
              {s.name}{!s.enabled && <span className="muted small"> (disabled)</span>}
            </label>
          ))}
        </fieldset>
        <div className="form-grid">
          <label>From <input type="date" value={form.from} onChange={(e) => set("from", e.target.value)} /></label>
          <label>To (not included) <input type="date" value={form.to} onChange={(e) => set("to", e.target.value)} /></label>
          <label>Window, hours <input inputMode="numeric" value={form.windowHours} onChange={(e) => set("windowHours", e.target.value)} /></label>
          <label>Article limit <input inputMode="numeric" value={form.maxArticles} onChange={(e) => set("maxArticles", e.target.value)} /></label>
        </div>
        <p className="hint">Each source is asked for each window in turn, so a long range with many sources makes many requests. Providers that rate-limit will slow it down.</p>
        {start.isError && <ErrorNote error={start.error} />}
        {start.isSuccess && <p className="notice" role="status">Started {start.data.id}.</p>}
        <div className="actions">
          <button className="primary" type="submit" disabled={problems.length > 0 || start.isPending}>Start backfill</button>
          {form.sources.length > 0 && problems.length > 0 && <span className="muted small">{problems[0]}</span>}
        </div>
      </form>
    </section>
  );
}

function ReprocessSection() {
  const qc = useQueryClient();
  const [form, setForm] = useState<ReprocessForm>(emptyReprocess());
  const jobs = useJobs("reprocess");
  const control = useControl("reprocess");
  const start = useMutation({
    mutationFn: async () => unwrap(await api.POST("/admin/reprocesses", {
      body: { limit: Number(form.limit), batch_size: Number(form.batchSize), concurrency: Number(form.concurrency) }, headers: authHeaders(),
    })),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["admin", "jobs", "reprocess"] }),
  });
  const problems = reprocessProblems(form);
  const set = <K extends keyof ReprocessForm>(k: K, v: ReprocessForm[K]) => setForm((f) => ({ ...f, [k]: v }));
  const submit = () => {
    if (problems.length > 0) return;
    if (Number(form.limit) === 0 && !window.confirm("Reprocess every stored article that needs it? Each one is a model call.")) return;
    start.mutate();
  };

  return (
    <section aria-labelledby="reprocesses">
      <h2 id="reprocesses">Reprocess</h2>
      <p className="muted">
        Classify stored articles again after a model or prompt change. Only articles with no extraction for the current model, prompt
        and schema are picked up, and each one is a model call. Incidents that no longer have supporting articles are withdrawn.
      </p>
      <JobTable kind="reprocess" jobs={jobs} control={control} />
      <form className="card" aria-label="Start a reprocess run" onSubmit={(e) => { e.preventDefault(); submit(); }}>
        <h3>Start a reprocess run</h3>
        <div className="form-grid">
          <label>Article limit <input inputMode="numeric" value={form.limit} onChange={(e) => set("limit", e.target.value)} /><span className="hint">0 means all of them.</span></label>
          <label>Batch size <input inputMode="numeric" value={form.batchSize} onChange={(e) => set("batchSize", e.target.value)} /></label>
          <label>At once <input inputMode="numeric" value={form.concurrency} onChange={(e) => set("concurrency", e.target.value)} /></label>
        </div>
        {start.isError && <ErrorNote error={start.error} />}
        {start.isSuccess && <p className="notice" role="status">Started {start.data.id}.</p>}
        <div className="actions">
          <button className="primary" type="submit" disabled={problems.length > 0 || start.isPending}>Start reprocess</button>
          {problems.length > 0 && <span className="muted small">{problems[0]}</span>}
        </div>
      </form>
    </section>
  );
}

function JobTable({ kind, jobs, control }: {
  kind: Kind;
  jobs: ReturnType<typeof useJobs>;
  control: ReturnType<typeof useControl>;
}) {
  if (jobs.isPending) return <Loading what={`${kind} runs`} />;
  if (jobs.isError) return <ErrorNote error={jobs.error} />;
  if (jobs.data.length === 0) return <p className="card">No {kind} runs yet.</p>;
  return (
    <>
      {control.isError && <ErrorNote error={control.error} />}
      <div className="scroll">
        <table className="data">
          <thead><tr><th>Run</th><th>Status</th><th>Progress</th><th></th></tr></thead>
          <tbody>
            {jobs.data.map((j) => <JobRow key={j.id} job={j} control={control} />)}
          </tbody>
        </table>
      </div>
    </>
  );
}

function JobRow({ job: j, control }: { job: Job; control: ReturnType<typeof useControl> }) {
  const phase = phaseText(j);
  return (
    <tr>
      <th scope="row">
        <code>{j.id}</code>
        <div className="muted small">started {ago(j.started_at)}{j.closed_at && `, ended ${ago(j.closed_at)}`}</div>
      </th>
      <td><JobStatusBadge job={j} /></td>
      <td className="wrap">
        {progressText(j)}
        {phase && <div className="muted small">{phase}</div>}
      </td>
      <td className="actions">
        {controlsFor(j).map((a) => (
          <button
            key={a} type="button" disabled={control.isPending} className={a === "cancel" ? "danger" : undefined}
            onClick={() => { if (a !== "cancel" || window.confirm(`Cancel ${j.id}? Work already done is kept.`)) control.mutate({ id: j.id, action: a }); }}
          >{a === "pause" ? "Pause" : a === "resume" ? "Resume" : "Cancel"}</button>
        ))}
      </td>
    </tr>
  );
}
