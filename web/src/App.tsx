import { useQuery } from "@tanstack/react-query";
import { NavLink, Route, Routes, useLocation } from "react-router-dom";
import { api, unwrap } from "./api/client";
import IncidentPage from "./pages/IncidentPage";
import MapPage from "./pages/MapPage";
import OpsPage from "./pages/OpsPage";
import ReviewsPage from "./pages/ReviewsPage";
import TrendsPage from "./pages/TrendsPage";

function PendingBadge() {
  const q = useQuery({
    queryKey: ["pipeline", "badge"],
    queryFn: async () => unwrap(await api.GET("/ops/pipeline", { params: { query: { days: 1 } } })),
    refetchInterval: 60_000,
  });
  const n = q.data?.counts.pending_reviews ?? 0;
  return n > 0 ? <span className="count" aria-label={`${n} pending`}>{n}</span> : null;
}

export default function App() {
  const { search } = useLocation();
  // Filters travel between the map and trends pages in the query string.
  const keep = (path: string) => ({ pathname: path, search });
  return (
    <>
      <header className="top">
        <strong className="brand">Collision Tracker</strong>
        <nav aria-label="Main">
          <NavLink to={keep("/")} end>Map</NavLink>
          <NavLink to={keep("/trends")}>Trends</NavLink>
          <NavLink to="/reviews">Reviews <PendingBadge /></NavLink>
          <NavLink to="/ops">Operations</NavLink>
        </nav>
      </header>
      <main>
        <Routes>
          <Route path="/" element={<MapPage />} />
          <Route path="/trends" element={<TrendsPage />} />
          <Route path="/incidents/:id" element={<IncidentPage />} />
          <Route path="/reviews" element={<ReviewsPage />} />
          <Route path="/ops" element={<OpsPage />} />
          <Route path="*" element={<p>Page not found.</p>} />
        </Routes>
      </main>
    </>
  );
}
