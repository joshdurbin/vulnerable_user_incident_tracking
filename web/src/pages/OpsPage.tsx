import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { adminToken, api, authHeaders, unwrap, type SourceInfo } from "../api/client";
import { ErrorNote, Loading, StatCard } from "../components/bits";
import { label } from "../format";

export default function OpsPage() {
  const qc = useQueryClient();
  const sources = useQuery({
    queryKey: ["sources"],
    queryFn: async () => unwrap(await api.GET("/ops/sources", { params: { query: { days: 30 } } })),
    refetchInterval: 30_000,
  });
  const pipe = useQuery({
    queryKey: ["pipeline"],
    queryFn: async () => unwrap(await api.GET("/ops/pipeline")),
    refetchInterval: 30_000,
  });
  const control = useMutation({
    mutationFn: async (v: { slug: string; action: "pause" | "resume" | "trigger" }) => {
      const r = await api.POST("/ops/sources/{slug}/schedule", { params: { path: { slug: v.slug } }, body: { action: v.action }, headers: authHeaders() });
      if (r.error) throw new Error((r.error as { error?: string }).error ?? "request failed");
    },
    onSuccess: () => qc.invalidateQueries({ queryKey: ["sources"] }),
  });

  return (
    <>
      <h1>Operations</h1>
      {pipe.isError && <ErrorNote error={pipe.error} />}
      {pipe.data && (
        <div className="stats">
          <StatCard title="URLs discovered" value={pipe.data.counts.discovered_urls.toLocaleString()} />
          <StatCard title="Articles fetched" value={pipe.data.counts.articles.toLocaleString()} hint={`${pipe.data.counts.failed_articles} failed`} />
          <StatCard title="Classified" value={pipe.data.counts.extractions.toLocaleString()} />
          <StatCard title="Awaiting review" value={pipe.data.counts.pending_reviews.toLocaleString()} />
        </div>
      )}

      <section className="card">
        <h2>Discovery sources</h2>
        <p className="muted small">
          Which sources are worth their cost: duplicates, exclusive finds, fetch success and relevance over the last 30 days. Pause and trigger act on
          the source's Temporal Schedule and need the admin token (set on the Reviews page).
          {!adminToken.get() && " No token is set in this tab."}
        </p>
        {sources.isPending && <Loading what="sources" />}
        {sources.isError && <ErrorNote error={sources.error} />}
        {control.isError && <ErrorNote error={control.error} />}
        {sources.data && (
          <div className="scroll">
            <table className="data">
              <thead>
                <tr><th>Source</th><th>Type</th><th>Status</th><th>Next run</th><th className="num">URLs</th><th className="num">Exclusive</th><th className="num">Fetch ok</th><th className="num">Relevant</th><th className="num">Incidents</th><th></th></tr>
              </thead>
              <tbody>{sources.data.map((s) => <SourceRow key={s.slug} s={s} onAction={(action) => control.mutate({ slug: s.slug, action })} busy={control.isPending} />)}</tbody>
            </table>
          </div>
        )}
      </section>

    </>
  );
}

const pct = (v?: number) => (v == null ? "–" : `${Math.round(v * 100)}%`);

function SourceRow({ s, onAction, busy }: { s: SourceInfo; onAction: (a: "pause" | "resume" | "trigger") => void; busy: boolean }) {
  const paused = s.schedule?.paused;
  const status = !s.enabled ? "Disabled" : s.consecutive_failures > 0 ? `Failing (${s.consecutive_failures})` : paused ? "Paused" : s.schedule ? "Running" : "No schedule";
  return (
    <tr>
      <th scope="row">{s.name}<div className="muted small">{s.slug}</div></th>
      <td>{label(s.type)}</td>
      <td title={s.error_state ?? undefined}>{status}</td>
      <td>{s.schedule?.next_run ? new Date(s.schedule.next_run).toLocaleString() : "–"}</td>
      <td className="num">{s.unique_urls ?? 0}</td>
      <td className="num">{s.exclusive_urls ?? 0}</td>
      <td className="num">{pct(s.fetch_success_rate)}</td>
      <td className="num">{pct(s.relevant_rate)}</td>
      <td className="num">{s.incidents ?? 0}</td>
      <td className="actions">
        {s.schedule && (paused
          ? <button type="button" disabled={busy} onClick={() => onAction("resume")}>Resume</button>
          : <button type="button" disabled={busy} onClick={() => onAction("pause")}>Pause</button>)}
        {s.schedule && <button type="button" disabled={busy} onClick={() => onAction("trigger")}>Run now</button>}
      </td>
    </tr>
  );
}
