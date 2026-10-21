import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { Link } from "react-router-dom";
import { actorName } from "../actor";
import { adminToken, api, authHeaders, unwrap, type Incident, type Review } from "../api/client";
import { ErrorNote, Loading, SeverityBadge } from "../components/bits";
import { casualties, label, when, where } from "../format";


/** Fields shown side by side; differing values are highlighted so the reviewer sees what to judge. */
export function comparison(a: Incident, b: Incident): { field: string; a: string; b: string; differs: boolean }[] {
  const rows: [string, string, string][] = [
    ["When", when(a), when(b)],
    ["Where", where(a), where(b)],
    ["Road user", a.vru_categories.map(label).join(", ") || "Unknown", b.vru_categories.map(label).join(", ") || "Unknown"],
    ["Vehicle", a.vehicle_categories.map(label).join(", ") || "Unknown", b.vehicle_categories.map(label).join(", ") || "Unknown"],
    ["Outcome", casualties(a), casualties(b)],
    ["Hit and run", label(a.hit_and_run), label(b.hit_and_run)],
  ];
  return rows.map(([field, x, y]) => ({ field, a: x, b: y, differs: x !== y }));
}

export default function ReviewsPage() {
  const qc = useQueryClient();
  const [token, setToken] = useState(adminToken.get());
  const [actor, setActor] = useState(actorName.get());
  const q = useQuery({
    queryKey: ["reviews"],
    queryFn: async () => unwrap(await api.GET("/reviews", { params: { query: { status: "pending", limit: 50 } } })),
  });

  return (
    <>
      <h1>Review queue</h1>
      <p className="muted">
        Each pair below may be the same collision reported twice. The system was not sure enough to merge them. Merging combines the records and
        keeps both sets of sources; keeping them separate leaves both as they are. Either way the decision is recorded.
      </p>
      <div className="filters">
        <label>Your name <input value={actor} onChange={(e) => { setActor(e.target.value); actorName.set(e.target.value); }} placeholder="recorded with each decision" /></label>
        <label>Admin token <input type="password" value={token} onChange={(e) => { setToken(e.target.value); adminToken.set(e.target.value); }} autoComplete="off" /></label>
      </div>
      {q.isPending && <Loading what="reviews" />}
      {q.isError && <ErrorNote error={q.error} />}
      {q.data?.length === 0 && <p className="card">Nothing to review.</p>}
      {q.data?.map((r) => (
        <ReviewCard key={r.id} review={r} actor={actor} onDone={() => qc.invalidateQueries({ queryKey: ["reviews"] })} />
      ))}
    </>
  );
}

export function ReviewCard({ review, actor, onDone }: { review: Review; actor: string; onDone: () => void }) {
  const decide = useMutation({
    mutationFn: async (approve: boolean) =>
      unwrap(await api.POST("/reviews/{id}/decision", { params: { path: { id: review.id } }, body: { approve, actor }, headers: authHeaders() })),
    onSuccess: onDone,
  });
  const rows = comparison(review.new_incident, review.candidate_incident);
  const disabled = !actor.trim() || decide.isPending;
  return (
    <section className="card review" aria-label={`Review ${review.id}`}>
      <header className="card-head">
        <h2>Possible duplicate <span className="muted small">match score {review.score.toFixed(2)}</span></h2>
      </header>
      <table className="compare">
        <thead>
          <tr>
            <th></th>
            <th><SeverityBadge severity={review.new_incident.severity} /> New report · <Link to={`/incidents/${review.new_incident.id}`}>open</Link></th>
            <th><SeverityBadge severity={review.candidate_incident.severity} /> Existing incident · <Link to={`/incidents/${review.candidate_incident.id}`}>open</Link></th>
          </tr>
        </thead>
        <tbody>
          {rows.map((r) => (
            <tr key={r.field} className={r.differs ? "differs" : ""}>
              <th scope="row">{r.field}</th>
              <td>{r.a}</td>
              <td>{r.b}</td>
            </tr>
          ))}
        </tbody>
      </table>
      {decide.isError && <ErrorNote error={decide.error} />}
      <div className="actions">
        <button type="button" className="primary" disabled={disabled} onClick={() => decide.mutate(true)}>Same collision: merge</button>
        <button type="button" disabled={disabled} onClick={() => decide.mutate(false)}>Different collisions: keep separate</button>
        {!actor.trim() && <span className="muted small">Enter your name to decide.</span>}
      </div>
    </section>
  );
}
