import type { Incident } from "./api/client";

export const label = (s: string | null | undefined): string =>
  s ? s.replace(/_/g, " ").replace(/^\w/, (c) => c.toUpperCase()) : "Unknown";

export const SEVERITY_COLOR: Record<string, string> = {
  fatal: "#c92a2a",
  serious: "#e8590c",
  minor: "#f2b705",
  unknown: "#868e96",
};

export function where(i: Pick<Incident, "street_address" | "city" | "state">): string {
  return [i.street_address, i.city, i.state].filter(Boolean).join(", ") || "Location unknown";
}

export function when(i: Pick<Incident, "occurred_on" | "occurred_at" | "occurred_precision">): string {
  if (!i.occurred_on) return "Date unknown";
  // occurred_on is a calendar date: format it in UTC so it never shifts a day in other time zones.
  const d = new Date(`${i.occurred_on}T00:00:00Z`);
  const date = d.toLocaleDateString(undefined, { year: "numeric", month: "short", day: "numeric", timeZone: "UTC" });
  if (i.occurred_precision === "datetime" && i.occurred_at) {
    return `${date}, ${new Date(i.occurred_at).toLocaleTimeString(undefined, { hour: "numeric", minute: "2-digit" })}`;
  }
  return date;
}

export function casualties(i: Pick<Incident, "injured_count" | "fatality_count">): string {
  const parts: string[] = [];
  if (i.fatality_count != null) parts.push(`${i.fatality_count} killed`);
  if (i.injured_count != null) parts.push(`${i.injured_count} injured`);
  return parts.join(", ") || "Outcome not reported";
}

export const WEEKDAYS = ["Mon", "Tue", "Wed", "Thu", "Fri", "Sat", "Sun"];
export const MONTHS = ["Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"];

/** The API returns ISO weekday ("1".."7") and month ("01".."12") keys, and only for days that occur. Fill the gaps. */
export function fillCalendar(rows: { key: string; incidents: number }[], names: string[], zeroBased: boolean): { name: string; incidents: number }[] {
  const byKey = new Map(rows.map((r) => [Number(r.key), r.incidents]));
  return names.map((name, idx) => ({ name, incidents: byKey.get(zeroBased ? idx : idx + 1) ?? 0 }));
}
