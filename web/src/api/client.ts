import createClient from "openapi-fetch";
import type { components, paths } from "./schema";

export const api = createClient<paths>({
  // Absolute so it works wherever a Request is built (browsers resolve a relative URL; Node does not).
  baseUrl: `${typeof window === "undefined" ? "http://localhost" : window.location.origin}/v1`,
  // The spec declares array query params as comma-separated (form, explode: false).
  // Look fetch up per call (not at import) so tests can replace it.
  fetch: (req) => globalThis.fetch(req),
  querySerializer: { array: { style: "form", explode: false } },
});

export type Incident = components["schemas"]["Incident"];
export type IncidentDetail = components["schemas"]["IncidentDetail"];
export type Review = components["schemas"]["Review"];
export type SourceDetail = components["schemas"]["SourceDetail"];
export type SourceInput = components["schemas"]["SourceInput"];
export type SourceType = components["schemas"]["SourceType"];
export type SourceRun = components["schemas"]["SourceRun"];
export type SourceTestResult = components["schemas"]["SourceTestResult"];
export type Query = components["schemas"]["Query"];
export type Job = components["schemas"]["Job"];
export type BackfillRequest = components["schemas"]["BackfillRequest"];
export type HealthState = components["schemas"]["Health"]["state"];

/** An API failure: the message the server gave, the HTTP status, and every validation problem when it listed them. */
export class ApiError extends Error {
  constructor(message: string, readonly status: number, readonly problems: string[] = []) {
    super(message);
    this.name = "ApiError";
  }
}

/** Turn an openapi-fetch result into data or a thrown ApiError with the API's message. */
export function unwrap<T>(r: { data?: T; error?: unknown; response: Response }): T {
  if (r.error !== undefined || r.data === undefined) {
    const e = r.error as { error?: string; problems?: string[] } | undefined;
    throw new ApiError(e?.error ?? `request failed (${r.response.status})`, r.response.status, e?.problems ?? []);
  }
  return r.data;
}

/** For calls that return no body (204): throw on failure, otherwise nothing. */
export function unwrapEmpty(r: { error?: unknown; response: Response }): void {
  if (r.error !== undefined || !r.response.ok) {
    const e = r.error as { error?: string; problems?: string[] } | undefined;
    throw new ApiError(e?.error ?? `request failed (${r.response.status})`, r.response.status, e?.problems ?? []);
  }
}

/** Admin token lives in sessionStorage only: it is cleared when the tab closes. */
const TOKEN_KEY = "collision.adminToken";
export const adminToken = {
  get: () => { try { return sessionStorage.getItem(TOKEN_KEY) ?? ""; } catch { return ""; } },
  set: (v: string) => { try { v ? sessionStorage.setItem(TOKEN_KEY, v) : sessionStorage.removeItem(TOKEN_KEY); } catch { /* storage unavailable */ } },
};
export const authHeaders = (): Record<string, string> => {
  const t = adminToken.get();
  return t ? { Authorization: `Bearer ${t}` } : {};
};
