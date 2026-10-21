import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "react-router-dom";
import { api, authHeaders, unwrap } from "../../api/client";
import { HealthBadge } from "../../components/admin";
import { ErrorNote, Loading, StatCard } from "../../components/bits";
import { HEALTH_LABEL, HEALTH_ORDER, ago, healthSummary, needsAttention } from "./adminForm";

/** How the search index is doing, and a way to rewrite it from scratch. */
function SearchIndexCard() {
  const qc = useQueryClient();
  const status = useQuery({
    queryKey: ["admin", "search"],
    queryFn: async () => unwrap(await api.GET("/admin/search", { headers: authHeaders() })),
    refetchInterval: 30_000,
  });
  const reindex = useMutation({
    mutationFn: async () => unwrap(await api.POST("/admin/search/reindex", { headers: authHeaders() })),
    onSuccess: (s) => qc.setQueryData(["admin", "search"], s),
  });
  const s = status.data;
  return (
    <section className="card">
      <h2>Search index</h2>
      {status.isPending && <Loading what="search status" />}
      {status.isError && <ErrorNote error={status.error} />}
      {s && !s.enabled && <p className="muted">Search is not configured, so the search box on the map is off. Set <code>COLLISION_SEARCH_URL</code> to turn it on.</p>}
      {s?.enabled && (
        <>
          {s.error && <p className="error" role="alert">{s.error}. Searches fail until it is back.</p>}
          <dl className="facts">
            <dt>Incidents indexed</dt><dd>{s.documents == null ? "unknown" : s.documents.toLocaleString()}</dd>
            <dt>Waiting to be indexed</dt><dd>{s.pending.toLocaleString()}</dd>
            <dt>Last written</dt><dd>{ago(s.indexed_at)}</dd>
          </dl>
          <p className="muted small">New and changed incidents are searchable about a minute after they are saved.</p>
          {reindex.isError && <ErrorNote error={reindex.error} />}
          <div className="actions">
            <button
              type="button" disabled={reindex.isPending}
              onClick={() => { if (window.confirm("Rewrite every incident into the search index? Search keeps working meanwhile.")) reindex.mutate(); }}
            >Rebuild index</button>
          </div>
        </>
      )}
    </section>
  );
}

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

      <SearchIndexCard />
    </>
  );
}
