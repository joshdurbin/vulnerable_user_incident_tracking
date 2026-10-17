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
export type SourceInfo = components["schemas"]["SourceInfo"];

/** Turn an openapi-fetch result into data or a thrown Error with the API's message. */
export function unwrap<T>(r: { data?: T; error?: unknown; response: Response }): T {
  if (r.error !== undefined || r.data === undefined) {
    const msg = (r.error as { error?: string } | undefined)?.error;
    throw new Error(msg ?? `request failed (${r.response.status})`);
  }
  return r.data;
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
