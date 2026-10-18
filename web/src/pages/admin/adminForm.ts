import type { HealthState, SourceDetail, SourceInput, SourceType } from "../../api/client";

/** The source form as strings, exactly as typed. Conversion to the API shape happens in inputFromForm. */
export interface SourceFormState {
  slug: string;
  name: string;
  type: string;
  url: string;
  pollMinutes: string;
  enabled: boolean;
  country: string;
  states: string;
  cities: string;
  allowedDomains: string; // one per line
  urlPatterns: string; // one per line
  maxPages: string;
  maxDepth: string;
  render: string;
}

export const emptyForm = (type = ""): SourceFormState => ({
  slug: "", name: "", type, url: "", pollMinutes: "30", enabled: true,
  country: "", states: "", cities: "", allowedDomains: "", urlPatterns: "", maxPages: "", maxDepth: "", render: "",
});

/** Split on commas, trimming blanks. */
export const csv = (s: string): string[] => s.split(",").map((x) => x.trim()).filter(Boolean);
/** Split on lines, trimming blanks. */
export const lines = (s: string): string[] => s.split("\n").map((x) => x.trim()).filter(Boolean);

export function formFromDetail(d: SourceDetail): SourceFormState {
  return {
    slug: d.slug, name: d.name, type: d.type, url: d.url ?? "", pollMinutes: String(Math.round(d.poll_interval_seconds / 60)), enabled: d.enabled,
    country: d.coverage?.country ?? "", states: (d.coverage?.states ?? []).join(", "), cities: (d.coverage?.cities ?? []).join(", "),
    allowedDomains: (d.crawl?.allowed_domains ?? []).join("\n"), urlPatterns: (d.crawl?.url_patterns ?? []).join("\n"),
    maxPages: d.crawl?.max_pages ? String(d.crawl.max_pages) : "", maxDepth: d.crawl?.max_depth ? String(d.crawl.max_depth) : "",
    render: d.crawl?.render ?? "",
  };
}

const num = (s: string): number | undefined => {
  const n = Number.parseInt(s, 10);
  return Number.isFinite(n) && n > 0 ? n : undefined;
};

/**
 * Build the API body. Only the fields the source type uses are sent, so switching a form from
 * "index page" to "RSS feed" cannot leave stale crawl settings behind on the saved source.
 */
export function inputFromForm(f: SourceFormState, t: SourceType | undefined): SourceInput {
  const out: SourceInput = {
    slug: f.slug.trim(), name: f.name.trim(), type: f.type, enabled: f.enabled,
    poll_interval_seconds: Math.round((Number.parseFloat(f.pollMinutes) || 0) * 60),
  };
  if (t?.url === "required") out.url = f.url.trim();
  const coverage = { country: f.country.trim() || undefined, states: csv(f.states), cities: csv(f.cities) };
  if (coverage.country || coverage.states.length || coverage.cities.length) {
    out.coverage = { country: coverage.country, states: coverage.states.length ? coverage.states : undefined, cities: coverage.cities.length ? coverage.cities : undefined };
  }
  const c = t?.crawl;
  if (c) {
    const crawl: NonNullable<SourceInput["crawl"]> = {};
    if (c.allowed_domains && lines(f.allowedDomains).length) crawl.allowed_domains = lines(f.allowedDomains);
    if (c.url_patterns && lines(f.urlPatterns).length) crawl.url_patterns = lines(f.urlPatterns);
    if (c.max_pages && num(f.maxPages)) crawl.max_pages = num(f.maxPages);
    if (c.max_depth && num(f.maxDepth)) crawl.max_depth = num(f.maxDepth);
    if (c.render && f.render) crawl.render = f.render as NonNullable<SourceInput["crawl"]>["render"];
    if (Object.keys(crawl).length) out.crawl = crawl;
  }
  return out;
}

/** Cheap checks so an obviously incomplete form is not sent. The server validates everything again and is the authority. */
export function quickProblems(f: SourceFormState, t: SourceType | undefined, creating: boolean): string[] {
  const p: string[] = [];
  if (creating && !f.slug.trim()) p.push("Give the source a slug (its permanent short name).");
  if (!f.name.trim()) p.push("Give the source a name.");
  if (!t) p.push("Choose a source type.");
  if (t?.url === "required" && !f.url.trim()) p.push("This type needs a URL.");
  if (!(Number.parseFloat(f.pollMinutes) >= 1)) p.push("Poll at most once a minute: the interval must be at least 1 minute.");
  if (t?.crawl.require_domains && !lines(f.allowedDomains).length) p.push("This type needs at least one allowed domain.");
  return p;
}

export const HEALTH_ORDER: HealthState[] = ["failing", "stale", "degraded", "waiting", "paused", "disabled", "healthy"];

export const HEALTH_LABEL: Record<HealthState, string> = {
  healthy: "Healthy", degraded: "Degraded", failing: "Failing", stale: "Stale", paused: "Paused", waiting: "Waiting", disabled: "Disabled",
};

/** How many sources are in each health state. */
export function healthSummary(sources: { health: { state: HealthState } }[]): Record<HealthState, number> {
  const out = Object.fromEntries(HEALTH_ORDER.map((s) => [s, 0])) as Record<HealthState, number>;
  for (const s of sources) out[s.health.state] += 1;
  return out;
}

/** True for the states that need a person to look. */
export const needsAttention = (s: HealthState): boolean => s === "failing" || s === "stale" || s === "degraded";

export function intervalLabel(seconds: number): string {
  if (seconds % 86400 === 0) return `${seconds / 86400} d`;
  if (seconds % 3600 === 0) return `${seconds / 3600} h`;
  if (seconds % 60 === 0) return `${seconds / 60} min`;
  return `${seconds} s`;
}

export function ago(iso: string | null | undefined, now = Date.now()): string {
  if (!iso) return "never";
  const s = Math.max(0, Math.round((now - new Date(iso).getTime()) / 1000));
  if (s < 60) return "just now";
  if (s < 3600) return `${Math.floor(s / 60)} min ago`;
  if (s < 86400) return `${Math.floor(s / 3600)} h ago`;
  return `${Math.floor(s / 86400)} d ago`;
}

export const percent = (v: number | undefined): string => (v == null ? "–" : `${Math.round(v * 100)}%`);
export const seconds = (v: number | undefined): string => (v == null ? "–" : v < 10 ? `${v.toFixed(1)} s` : `${Math.round(v)} s`);

/** "in 15 min" for a time ahead, "overdue" for one already past. */
export function until(iso: string | null | undefined, now = Date.now()): string {
  if (!iso) return "–";
  const s = Math.round((new Date(iso).getTime() - now) / 1000);
  if (s < 0) return "overdue";
  if (s < 60) return "in under a minute";
  if (s < 3600) return `in ${Math.floor(s / 60)} min`;
  if (s < 86400) return `in ${Math.floor(s / 3600)} h`;
  return `in ${Math.floor(s / 86400)} d`;
}

/** The provider's own name for a type ("RSS feed", "GDELT"), falling back to the raw value for a type this UI does not know. */
export function typeLabel(types: { type: string; label: string }[], t: string): string {
  return types.find((x) => x.type === t)?.label ?? t;
}
