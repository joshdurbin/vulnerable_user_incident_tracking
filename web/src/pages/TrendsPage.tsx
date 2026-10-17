import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { Bar, BarChart, CartesianGrid, Cell, Legend, ResponsiveContainer, Tooltip, XAxis, YAxis } from "recharts";
import { api, unwrap } from "../api/client";
import { ErrorNote, Loading, StatCard } from "../components/bits";
import { FilterBar } from "../components/FilterBar";
import { toQuery, useFilters } from "../filters";
import { MONTHS, SEVERITY_COLOR, WEEKDAYS, fillCalendar, label } from "../format";

type Bucket = "day" | "week" | "month" | "year";
type Dim = "vru" | "vehicle" | "severity" | "weekday" | "month" | "city" | "state" | "hit_and_run";

export default function TrendsPage() {
  const [filters] = useFilters();
  const [bucket, setBucket] = useState<Bucket>("month");
  const query = toQuery(filters);

  const summary = useQuery({
    queryKey: ["summary", filters],
    queryFn: async () => unwrap(await api.GET("/stats/summary", { params: { query } })),
  });
  const series = useQuery({
    queryKey: ["timeseries", filters, bucket],
    queryFn: async () => unwrap(await api.GET("/stats/timeseries", { params: { query: { ...query, bucket } } })),
  });

  return (
    <>
      <FilterBar />
      {summary.isError && <ErrorNote error={summary.error} />}
      {summary.data && (
        <div className="stats">
          <StatCard title="Incidents" value={summary.data.incidents.toLocaleString()} hint={`from ${summary.data.articles.toLocaleString()} articles`} />
          <StatCard title="People killed" value={summary.data.fatalities.toLocaleString()} hint={`in ${summary.data.fatal_incidents} fatal incidents`} />
          <StatCard title="People injured" value={summary.data.injured.toLocaleString()} />
          <StatCard title="Hit and run" value={summary.data.hit_and_run.toLocaleString()} hint={pct(summary.data.hit_and_run, summary.data.incidents)} />
          <StatCard title="Corroborated" value={summary.data.corroborated.toLocaleString()} hint="reported by 2+ sources" />
        </div>
      )}

      <section className="card">
        <header className="card-head">
          <h2>Incidents over time</h2>
          <label>
            Group by{" "}
            <select value={bucket} onChange={(e) => setBucket(e.target.value as Bucket)}>
              {(["day", "week", "month", "year"] as const).map((b) => <option key={b} value={b}>{label(b)}</option>)}
            </select>
          </label>
        </header>
        {series.isPending && <Loading what="time series" />}
        {series.isError && <ErrorNote error={series.error} />}
        {series.data && series.data.length === 0 && <p className="muted">No incidents with a known date match these filters.</p>}
        {series.data && series.data.length > 0 && (
          <div className="chart" role="img" aria-label="Incidents, killed and injured over time">
            <ResponsiveContainer width="100%" height={280}>
              <BarChart data={series.data.map((p) => ({ ...p, bucket: p.bucket.slice(0, bucket === "year" ? 4 : bucket === "month" ? 7 : 10) }))}>
                <CartesianGrid strokeDasharray="3 3" vertical={false} />
                <XAxis dataKey="bucket" tick={{ fontSize: 12 }} />
                <YAxis allowDecimals={false} />
                <Tooltip />
                <Legend />
                <Bar dataKey="incidents" name="Incidents" fill="#495057" />
                <Bar dataKey="fatalities" name="Killed" fill={SEVERITY_COLOR.fatal} />
                <Bar dataKey="injured" name="Injured" fill={SEVERITY_COLOR.serious} />
              </BarChart>
            </ResponsiveContainer>
          </div>
        )}
      </section>

      <div className="grid2">
        <Breakdown title="By road user" dim="vru" />
        <Breakdown title="By severity" dim="severity" colors={SEVERITY_COLOR} />
        <Breakdown title="By day of week" dim="weekday" />
        <Breakdown title="By month of year" dim="month" />
        <Breakdown title="Top cities" dim="city" horizontal />
        <Breakdown title="By vehicle" dim="vehicle" horizontal />
      </div>
    </>
  );
}

const pct = (a: number, b: number) => (b > 0 ? `${Math.round((100 * a) / b)}% of incidents` : "");

function Breakdown({ title, dim, horizontal, colors }: { title: string; dim: Dim; horizontal?: boolean; colors?: Record<string, string> }) {
  const [filters] = useFilters();
  const q = useQuery({
    queryKey: ["breakdown", dim, filters],
    queryFn: async () => unwrap(await api.GET("/stats/breakdown", { params: { query: { ...toQuery(filters), by: dim, limit: dim === "city" ? 10 : 25 } } })),
  });
  const data = (() => {
    if (!q.data) return [];
    if (dim === "weekday") return fillCalendar(q.data, WEEKDAYS, false).map((d) => ({ ...d, key: d.name }));
    if (dim === "month") return fillCalendar(q.data, MONTHS, false).map((d) => ({ ...d, key: d.name }));
    return q.data.map((r) => ({ name: label(r.key), key: r.key, incidents: r.incidents }));
  })();
  return (
    <section className="card">
      <h2>{title}</h2>
      {q.isPending && <Loading what={title.toLowerCase()} />}
      {q.isError && <ErrorNote error={q.error} />}
      {q.data && data.every((d) => d.incidents === 0) && <p className="muted">No data.</p>}
      {q.data && data.some((d) => d.incidents > 0) && (
        <div className="chart" role="img" aria-label={title}>
          <ResponsiveContainer width="100%" height={horizontal ? Math.max(180, data.length * 28) : 220}>
            <BarChart data={data} layout={horizontal ? "vertical" : "horizontal"} margin={{ left: horizontal ? 40 : 0 }}>
              <CartesianGrid strokeDasharray="3 3" horizontal={!horizontal} vertical={horizontal} />
              {horizontal ? <YAxis type="category" dataKey="name" width={110} tick={{ fontSize: 12 }} /> : <XAxis dataKey="name" interval={0} tick={{ fontSize: 11 }} />}
              {horizontal ? <XAxis type="number" allowDecimals={false} /> : <YAxis allowDecimals={false} />}
              <Tooltip />
              <Bar dataKey="incidents" name="Incidents" fill="#495057">
                {colors && data.map((d) => <Cell key={d.key} fill={colors[d.key] ?? "#868e96"} />)}
              </Bar>
            </BarChart>
          </ResponsiveContainer>
        </div>
      )}
    </section>
  );
}
