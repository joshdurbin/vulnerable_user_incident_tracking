import { useQuery } from "@tanstack/react-query";
import { Navigate, NavLink, Route, Routes, useLocation } from "react-router-dom";
import { api, unwrap } from "./api/client";
import IncidentPage from "./pages/IncidentPage";
import MapPage from "./pages/MapPage";
import AdminLayout from "./pages/admin/AdminLayout";
import JobsPage from "./pages/admin/JobsPage";
import OverviewPage from "./pages/admin/OverviewPage";
import QueriesPage from "./pages/admin/QueriesPage";
import SourceDetailPage from "./pages/admin/SourceDetailPage";
import SourceFormPage from "./pages/admin/SourceFormPage";
import SourcesPage from "./pages/admin/SourcesPage";
import ReviewsPage from "./pages/ReviewsPage";
import TrendsPage from "./pages/TrendsPage";

function PendingBadge() {
  const q = useQuery({
    queryKey: ["pipeline", "badge"],
    queryFn: async () => unwrap(await api.GET("/ops/pipeline")),
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
          <NavLink to="/admin">Admin</NavLink>
        </nav>
      </header>
      <main>
        <Routes>
          <Route path="/" element={<MapPage />} />
          <Route path="/trends" element={<TrendsPage />} />
          <Route path="/incidents/:id" element={<IncidentPage />} />
          <Route path="/reviews" element={<ReviewsPage />} />
          <Route path="/admin" element={<AdminLayout />}>
            <Route index element={<OverviewPage />} />
            <Route path="sources" element={<SourcesPage />} />
            <Route path="sources/new" element={<SourceFormPage />} />
            <Route path="sources/:slug" element={<SourceDetailPage />} />
            <Route path="sources/:slug/edit" element={<SourceFormPage />} />
            <Route path="queries" element={<QueriesPage />} />
            <Route path="jobs" element={<JobsPage />} />
          </Route>
          <Route path="/ops" element={<Navigate to="/admin" replace />} />
          <Route path="*" element={<p>Page not found.</p>} />
        </Routes>
      </main>
    </>
  );
}
