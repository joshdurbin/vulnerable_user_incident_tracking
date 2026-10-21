import { useQuery } from "@tanstack/react-query";
import { lazy, Suspense, useState } from "react";
import { Link } from "react-router-dom";
import { api, unwrap } from "../api/client";
import { SeverityBadge, ErrorNote, Loading } from "../components/bits";
import { FilterBar } from "../components/FilterBar";
import { toListQuery, useFilters } from "../filters";
import { casualties, label, when, where } from "../format";

// MapLibre is large; load it only when the map page is opened.
const MapView = lazy(() => import("../components/MapView"));

export default function MapPage() {
  const [filters] = useFilters();
  const [selected, setSelected] = useState<string>();
  const q = useQuery({
    queryKey: ["incidents", "map", filters],
    queryFn: async () => unwrap(await api.GET("/incidents", { params: { query: { ...toListQuery(filters), limit: 1000 } } })),
  });
  const items = q.data?.items ?? [];
  const located = items.filter((i) => i.lat != null).length;

  return (
    <>
      <FilterBar search />
      <div className="map-layout">
        <div className="map-wrap">
          <Suspense fallback={<Loading what="map" />}>
            <MapView items={items} selectedId={selected} onSelect={setSelected} />
          </Suspense>
        </div>
        <aside className="side" aria-label="Incident list">
          {q.isPending && <Loading what="incidents" />}
          {q.isError && <ErrorNote error={q.error} />}
          {q.data && (
            <p className="muted">
              {q.data.total} incident{q.data.total === 1 ? "" : "s"}
              {filters.q && ` matching “${filters.q}”, best match first`}
              {q.data.total > items.length && ` (showing ${filters.q ? "best" : "newest"} ${items.length})`}. {located} on the map
              {items.length > located && `; ${items.length - located} have no coordinates yet and appear only here`}.
            </p>
          )}
          <ul className="incident-list">
            {items.map((i) => (
              <li key={i.id} className={i.id === selected ? "selected" : ""}>
                <button type="button" onClick={() => setSelected(i.id)} className="linklike">
                  <SeverityBadge severity={i.severity} /> <strong>{where(i)}</strong>
                </button>
                <div className="muted small">
                  {when(i)} · {i.vru_categories.map(label).join(", ") || "Road user unknown"} · {casualties(i)}
                </div>
                <div className="small">
                  <Link to={`/incidents/${i.id}`}>Details</Link> · {i.article_count} source{i.article_count === 1 ? "" : "s"}
                  {i.verification_status === "corroborated" && " · corroborated"}
                </div>
              </li>
            ))}
          </ul>
        </aside>
      </div>
    </>
  );
}
