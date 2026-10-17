import { useQuery } from "@tanstack/react-query";
import { Link, useParams } from "react-router-dom";
import { api, unwrap } from "../api/client";
import { ErrorNote, Loading, SeverityBadge } from "../components/bits";
import { useRuntimeConfig } from "../RuntimeConfig";
import { workflowUrl } from "../config";
import { casualties, label, when, where } from "../format";

export default function IncidentPage() {
  const { id = "" } = useParams();
  const cfg = useRuntimeConfig();
  const q = useQuery({
    queryKey: ["incident", id],
    queryFn: async () => unwrap(await api.GET("/incidents/{id}", { params: { path: { id } } })),
  });
  if (q.isPending) return <Loading what="incident" />;
  if (q.isError) return <ErrorNote error={q.error} />;
  const d = q.data;
  const i = d.incident;
  const temporal = workflowUrl(cfg, d.workflow_id);

  return (
    <article className="detail">
      <p><Link to="/">← Map</Link></p>
      <h1><SeverityBadge severity={i.severity} /> {where(i)}</h1>
      {d.merged_into && <p className="notice">This record was merged into <Link to={`/incidents/${d.merged_into}`}>another incident</Link>.</p>}
      {i.verification_status === "withdrawn" && <p className="notice">Withdrawn: the articles that supported this incident were reprocessed and no longer describe it.</p>}

      <dl className="facts">
        <dt>When</dt><dd>{when(i)}</dd>
        <dt>Where</dt><dd>{where(i)} <span className="muted">({label(i.location_precision)}{i.lat == null ? ", no coordinates" : ""})</span></dd>
        <dt>Road user</dt><dd>{i.vru_categories.map(label).join(", ") || "Unknown"}</dd>
        <dt>Vehicle</dt><dd>{i.vehicle_categories.map(label).join(", ") || "Unknown"}</dd>
        <dt>Outcome</dt><dd>{casualties(i)}</dd>
        <dt>Hit and run</dt><dd>{label(i.hit_and_run)}</dd>
        <dt>Facts</dt><dd>{label(i.facts_status)} · {label(i.verification_status)}</dd>
        {d.circumstances && (<><dt>Circumstances</dt><dd>{d.circumstances}</dd></>)}
      </dl>
      {temporal && <p><a href={temporal} target="_blank" rel="noreferrer">View in Temporal ↗</a></p>}

      <section className="card">
        <h2>Sources ({d.articles.length})</h2>
        <ul className="plain">
          {d.articles.map((a) => (
            <li key={a.article_id}>
              <a href={a.url} target="_blank" rel="noreferrer noopener">{a.title || a.url}</a>
              <div className="muted small">
                {a.publisher ?? "Unknown publisher"}
                {a.published_at && ` · ${new Date(a.published_at).toLocaleDateString()}`}
                {` · matched by ${a.matched_by} (${a.match_status}${a.match_score != null ? `, score ${a.match_score.toFixed(2)}` : ""})`}
              </div>
            </li>
          ))}
        </ul>
      </section>

      <section className="card">
        <h2>What each article reported</h2>
        <p className="muted small">The record above is built from these. Where reports disagree, later and larger counts win, and the conflict is logged below.</p>
        <ol className="timeline">
          {d.observations.map((o, n) => {
            const c = o.data as Record<string, unknown>;
            const cand = (c.candidate ?? c) as Record<string, unknown>;
            return (
              <li key={n}>
                <strong>{new Date(o.observed_at).toLocaleString()}</strong>
                <div className="small">
                  {[cand.street_address, cand.city].filter(Boolean).join(", ") || "Location not stated"} ·
                  {` injured ${val(cand.injured_count)}, killed ${val(cand.fatality_count)}`} · severity {String(cand.severity ?? "unknown")}
                </div>
              </li>
            );
          })}
        </ol>
      </section>

      <section className="card">
        <h2>History</h2>
        <ol className="timeline">
          {d.events.map((e) => (
            <li key={e.id}>
              <strong>{label(e.type)}</strong> <span className="muted small">by {e.actor} · {new Date(e.created_at).toLocaleString()}</span>
              {e.type === "conflict" && <pre className="small">{JSON.stringify(e.data, null, 1)}</pre>}
            </li>
          ))}
        </ol>
      </section>
    </article>
  );
}

const val = (v: unknown) => (v == null ? "?" : String(v));
