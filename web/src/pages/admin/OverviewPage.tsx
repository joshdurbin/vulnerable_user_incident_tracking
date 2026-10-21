import { useQuery } from "@tanstack/react-query";
import { Link } from "react-router-dom";
import { api, authHeaders, unwrap } from "../../api/client";
import { HealthBadge } from "../../components/admin";
import { ErrorNote, Loading, StatCard } from "../../components/bits";
import { HEALTH_LABEL, HEALTH_ORDER, ago, healthSummary, needsAttention } from "./adminForm";

export default function OverviewPage() {
  const pipe = useQuery({
    queryKey: ["pipeline"],
    queryFn: async () => unwrap(await api.GET("/ops/pipeline")),
    refetchInterval: 30_000,
  });
  const sources = useQuery({
    queryKey: ["admin", "sources"],
    queryFn: async () => unwrap(await api.GET("/admin/sources", { params: { query: { days: 30 } }, headers: authHeaders() })),
    refetchInterval: 30_000,
  });
  const summary = sources.data ? healthSummary(sources.data) : undefined;
  const attention = (sources.data ?? []).filter((s) => needsAttention(s.health.state));

  return (
    <>
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
        <h2>Source health</h2>
        {sources.isPending && <Loading what="sources" />}
        {sources.isError && <ErrorNote error={sources.error} />}
        {sources.data?.length === 0 && (
          <p>
            No sources are configured, so nothing is being collected. <Link to="/admin/sources/new">Add a source</Link>. (The API loads its configured seed list when it starts with no
            sources; if you expected one, check that <code>COLLISION_SOURCES_SEED_FILE</code> is set.)
          </p>
        )}
        {summary && sources.data && sources.data.length > 0 && (
          <>
            <ul className="health-counts" aria-label="Sources by health">
              {HEALTH_ORDER.filter((s) => summary[s] > 0).map((s) => (
                <li key={s}><HealthBadge state={s} /> <strong>{summary[s]}</strong> <span className="muted small">{HEALTH_LABEL[s].toLowerCase()}</span></li>
              ))}
            </ul>
            {attention.length === 0 ? <p className="muted">Nothing needs attention.</p> : (
              <>
                <h3>Needs attention</h3>
                <ul className="plain">
                  {attention.map((s) => (
                    <li key={s.slug}>
                      <HealthBadge state={s.health.state} /> <Link to={`/admin/sources/${s.slug}`}>{s.name}</Link>{" "}
                      <span className="muted small">{s.health.reason}{s.health.reason.includes("last success") ? "" : ` · last success ${ago(s.last_success_at)}`}</span>
                    </li>
                  ))}
                </ul>
              </>
            )}
          </>
        )}
      </section>
    </>
  );
}
