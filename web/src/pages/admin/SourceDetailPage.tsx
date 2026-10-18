import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useNavigate, useParams } from "react-router-dom";
import { api, authHeaders, unwrap, unwrapEmpty } from "../../api/client";
import { HealthBadge, RunStatusBadge } from "../../components/admin";
import { ErrorNote, Loading, StatCard } from "../../components/bits";
import { label } from "../../format";
import { useAdmin } from "./AdminLayout";
import { ago, intervalLabel, percent, seconds, typeLabel, until } from "./adminForm";
import { ScheduleButtons, useScheduleControl } from "./SourcesPage";

export default function SourceDetailPage() {
  const { slug = "" } = useParams();
  const qc = useQueryClient();
  const nav = useNavigate();
  const src = useQuery({
    queryKey: ["admin", "source", slug],
    queryFn: async () => unwrap(await api.GET("/admin/sources/{slug}", { params: { path: { slug } }, headers: authHeaders() })),
    refetchInterval: 30_000,
  });
  const runs = useQuery({
    queryKey: ["admin", "runs", slug],
    queryFn: async () => unwrap(await api.GET("/admin/sources/{slug}/runs", { params: { path: { slug }, query: { limit: 50 } }, headers: authHeaders() })),
    refetchInterval: 30_000,
  });
  const { types } = useAdmin();
  const control = useScheduleControl();
  const del = useMutation({
    mutationFn: async () => unwrapEmpty(await api.DELETE("/admin/sources/{slug}", { params: { path: { slug } }, headers: authHeaders() })),
    onSuccess: () => { qc.invalidateQueries({ queryKey: ["admin"] }); nav("/admin/sources"); },
  });

  if (src.isPending) return <Loading what="source" />;
  if (src.isError) return <ErrorNote error={src.error} />;
  const s = src.data;
  const covers = [s.coverage?.cities?.join(", "), s.coverage?.states?.join(", "), s.coverage?.country].filter(Boolean).join(" · ");

  return (
    <>
      <p><Link to="/admin/sources">← All sources</Link></p>
      <div className="card-head">
        <h2>{s.name} <HealthBadge state={s.health.state} reason={s.health.reason} /></h2>
        <div className="actions">
          <ScheduleButtons s={s} busy={control.isPending} onAction={(action) => control.mutate({ slug, action })} />
          <Link className="button" to={`/admin/sources/${slug}/edit`}>Edit</Link>
          <button
            type="button" className="danger" disabled={del.isPending}
            onClick={() => { if (window.confirm(`Delete "${s.name}", its schedule and its run history? Articles it found are kept.`)) del.mutate(); }}
          >Delete</button>
        </div>
      </div>
      <p className="muted">{s.health.reason}</p>
      {(control.isError || del.isError) && <ErrorNote error={control.error ?? del.error} />}

      <div className="stats">
        <StatCard title="Runs (30 d)" value={s.run_stats.runs.toLocaleString()} hint={`${s.run_stats.failures} failed, ${s.run_stats.partials} partial`} />
        <StatCard title="Success rate" value={s.run_stats.runs ? percent(s.run_stats.success_rate) : "–"} />
        <StatCard title="Run time" value={s.run_stats.runs ? seconds(s.run_stats.avg_seconds) : "–"} hint={s.run_stats.runs ? `p95 ${seconds(s.run_stats.p95_seconds)}` : undefined} />
        <StatCard title="New URLs" value={s.run_stats.new_urls.toLocaleString()} hint={`${s.run_stats.requests.toLocaleString()} upstream requests`} />
        <StatCard title="Fetch success" value={s.quality.articles_fetched ? percent(s.quality.fetch_success_rate) : "–"} hint={`${s.quality.articles_fetched} articles`} />
        <StatCard title="Relevant" value={s.quality.articles_fetched ? percent(s.quality.relevant_rate) : "–"} hint={`${s.quality.incidents} incidents`} />
        <StatCard title="Exclusive URLs" value={s.quality.exclusive_urls.toLocaleString()} hint="found by no other source" />
      </div>

      <section className="card">
        <h2>Configuration</h2>
        <dl className="facts">
          <dt>Type</dt><dd>{typeLabel(types, s.type)}</dd>
          {s.url && (<><dt>URL</dt><dd><a href={s.url} rel="noreferrer noopener" target="_blank">{s.url}</a></dd></>)}
          <dt>Runs every</dt><dd>{intervalLabel(s.poll_interval_seconds)}</dd>
          <dt>Enabled</dt><dd>{s.enabled ? "Yes" : "No"}</dd>
          {covers && (<><dt>Covers</dt><dd>{covers}</dd></>)}
          {s.crawl?.allowed_domains && (<><dt>Allowed domains</dt><dd>{s.crawl.allowed_domains.join(", ")}</dd></>)}
          {s.crawl?.url_patterns && (<><dt>URL patterns</dt><dd>{s.crawl.url_patterns.join(", ")}</dd></>)}
          {s.crawl?.max_pages != null && (<><dt>Max pages</dt><dd>{s.crawl.max_pages}</dd></>)}
          {s.crawl?.max_depth != null && (<><dt>Max depth</dt><dd>{s.crawl.max_depth}</dd></>)}
          {s.crawl?.render && (<><dt>JavaScript rendering</dt><dd>{label(s.crawl.render)}</dd></>)}
          <dt>Slug</dt><dd><code>{s.slug}</code></dd>
          <dt>Next run</dt><dd>{s.schedule?.next_run ? `${until(s.schedule.next_run)} (${new Date(s.schedule.next_run).toLocaleString()})` : s.schedule ? "–" : "no schedule: use Sync schedules on the Sources page"}</dd>
        </dl>
      </section>

      <section className="card">
        <h2>Recent runs</h2>
        {runs.isPending && <Loading what="runs" />}
        {runs.isError && <ErrorNote error={runs.error} />}
        {runs.data?.length === 0 && <p className="muted">This source has not run yet.</p>}
        {runs.data && runs.data.length > 0 && (
          <div className="scroll">
            <table className="data">
              <thead>
                <tr><th>Started</th><th>Result</th><th className="num">Took</th><th className="num">Found</th><th className="num">New</th><th className="num">Known</th><th className="num">Requests</th><th>Notes</th></tr>
              </thead>
              <tbody>
                {runs.data.map((r) => {
                  const took = (new Date(r.finished_at).getTime() - new Date(r.started_at).getTime()) / 1000;
                  const notes = [r.error, r.truncated ? "results were cut off" : "", ...r.warnings].filter(Boolean);
                  return (
                    <tr key={r.started_at}>
                      <td title={new Date(r.started_at).toLocaleString()}>{ago(r.started_at)}</td>
                      <td><RunStatusBadge status={r.status} /></td>
                      <td className="num">{seconds(took)}</td>
                      <td className="num">{r.found}</td>
                      <td className="num">{r.new_urls}</td>
                      <td className="num">{r.known_urls}</td>
                      <td className="num">{r.requests}</td>
                      <td className="wrap">{notes.length ? notes.join(" · ") : ""}</td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        )}
      </section>
    </>
  );
}
