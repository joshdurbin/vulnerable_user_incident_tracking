import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "react-router-dom";
import { api, authHeaders, unwrap, unwrapEmpty, type SourceDetail } from "../../api/client";
import { HealthBadge } from "../../components/admin";
import { ErrorNote, Loading } from "../../components/bits";
import { useAdmin } from "./AdminLayout";
import { ago, intervalLabel, percent, seconds, typeLabel, until } from "./adminForm";

export type ScheduleAction = "pause" | "resume" | "trigger";

/** Pause, resume and run-now, shared by the list and the detail page. */
export function useScheduleControl() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (v: { slug: string; action: ScheduleAction }) =>
      unwrapEmpty(await api.POST("/admin/sources/{slug}/schedule", { params: { path: { slug: v.slug } }, body: { action: v.action }, headers: authHeaders() })),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["admin"] }),
  });
}

export function ScheduleButtons({ s, busy, onAction }: { s: SourceDetail; busy: boolean; onAction: (a: ScheduleAction) => void }) {
  if (!s.schedule) return <span className="muted small">no schedule</span>;
  return (
    <>
      {s.schedule.paused
        ? <button type="button" disabled={busy} onClick={() => onAction("resume")}>Resume</button>
        : <button type="button" disabled={busy} onClick={() => onAction("pause")}>Pause</button>}
      <button type="button" disabled={busy} onClick={() => onAction("trigger")}>Run now</button>
    </>
  );
}

export default function SourcesPage() {
  const qc = useQueryClient();
  const sources = useQuery({
    queryKey: ["admin", "sources"],
    queryFn: async () => unwrap(await api.GET("/admin/sources", { params: { query: { days: 30 } }, headers: authHeaders() })),
    refetchInterval: 30_000,
  });
  const { types } = useAdmin();
  const control = useScheduleControl();
  const sync = useMutation({
    mutationFn: async () => unwrap(await api.POST("/admin/schedules/sync", { headers: authHeaders() })),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["admin"] }),
  });

  return (
    <>
      <div className="card-head">
        <p className="muted">
          Where articles come from, how each source is doing, and whether it is worth running. Quality columns cover the last 30 days.
        </p>
        <div className="actions">
          <button type="button" disabled={sync.isPending} onClick={() => sync.mutate()} title="Make every source's Temporal schedule match its configuration">Sync schedules</button>
          <Link className="button primary" to="/admin/sources/new">Add source</Link>
        </div>
      </div>
      {sync.isSuccess && <p className="notice" role="status">Schedules synced: {sync.data.created} created, {sync.data.updated} updated, {sync.data.removed} removed.</p>}
      {(sync.isError || control.isError) && <ErrorNote error={sync.error ?? control.error} />}
      {sources.isPending && <Loading what="sources" />}
      {sources.isError && <ErrorNote error={sources.error} />}
      {sources.data?.length === 0 && (
        <p className="card">
          No sources yet. <Link to="/admin/sources/new">Add one</Link>, or import a starter set with{" "}
          <code>collisionctl sources import --file docs/examples/sources.yaml</code>.
        </p>
      )}
      {sources.data && sources.data.length > 0 && (
        <div className="scroll">
          <table className="data">
            <thead>
              <tr>
                <th>Source</th><th>Health</th><th>Last success</th><th>Next run</th>
                <th className="num">Runs OK</th><th className="num">Avg time</th><th className="num">New URLs</th>
                <th className="num">Relevant</th><th className="num">Incidents</th><th></th>
              </tr>
            </thead>
            <tbody>
              {sources.data.map((s) => (
                <tr key={s.slug}>
                  <th scope="row">
                    <Link to={`/admin/sources/${s.slug}`}>{s.name}</Link>
                    <div className="muted small">{typeLabel(types, s.type)} · every {intervalLabel(s.poll_interval_seconds)}</div>
                  </th>
                  <td><HealthBadge state={s.health.state} reason={s.health.reason} /></td>
                  <td title={s.last_success_at ?? undefined}>{ago(s.last_success_at)}</td>
                  <td title={s.schedule?.next_run ? new Date(s.schedule.next_run).toLocaleString() : undefined}>{until(s.schedule?.next_run)}</td>
                  <td className="num">{s.run_stats.runs ? percent(s.run_stats.success_rate) : "–"}</td>
                  <td className="num">{s.run_stats.runs ? seconds(s.run_stats.avg_seconds) : "–"}</td>
                  <td className="num">{s.run_stats.new_urls.toLocaleString()}</td>
                  <td className="num">{s.quality.articles_fetched ? percent(s.quality.relevant_rate) : "–"}</td>
                  <td className="num">{s.quality.incidents.toLocaleString()}</td>
                  <td className="actions">
                    <ScheduleButtons s={s} busy={control.isPending} onAction={(action) => control.mutate({ slug: s.slug, action })} />
                    <Link className="button" to={`/admin/sources/${s.slug}/edit`}>Edit</Link>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </>
  );
}
