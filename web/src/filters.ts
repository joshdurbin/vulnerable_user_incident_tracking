import { useCallback, useMemo } from "react";
import { useSearchParams } from "react-router-dom";
import type { paths } from "./api/schema";

export interface Filters {
  from?: string;
  to?: string;
  state?: string;
  vru: VruCategory[];
  severity?: string;
  hit_and_run?: string;
}

// Typed against the API spec (itself checked against the classifier schema by a Go test), so a
// category the classifier never produces fails to compile instead of silently matching nothing.
export type VruCategory = NonNullable<NonNullable<paths["/incidents"]["get"]["parameters"]["query"]>["vru"]>[number];
export const VRU_OPTIONS: VruCategory[] = ["pedestrian", "bicyclist", "e_bike_rider", "scooter_rider", "motorcyclist", "moped_rider", "wheelchair_user", "skateboarder", "other"];
export const SEVERITIES = ["fatal", "serious", "minor", "unknown"];

export function parseFilters(p: URLSearchParams): Filters {
  // Unknown values (an old bookmark, a hand-edited URL) are dropped: the API would reject them.
  const vru = (p.get("vru") ?? "").split(",").filter((v): v is VruCategory => (VRU_OPTIONS as string[]).includes(v));
  return {
    from: p.get("from") || undefined,
    to: p.get("to") || undefined,
    state: p.get("state") || undefined,
    vru,
    severity: p.get("severity") || undefined,
    hit_and_run: p.get("hit_and_run") || undefined,
  };
}

/** Query object for the API; empty values are dropped so they are not sent. */
export function toQuery(f: Filters) {
  return {
    from: f.from,
    to: f.to,
    state: f.state,
    vru: f.vru.length ? f.vru : undefined,
    severity: f.severity as "fatal" | "serious" | "minor" | "unknown" | undefined,
    hit_and_run: f.hit_and_run as "yes" | "no" | "unknown" | undefined,
  };
}

/** Filters live in the URL, so a view can be bookmarked or shared and survives navigation between pages. */
export function useFilters(): [Filters, (patch: Partial<Filters>) => void] {
  const [params, setParams] = useSearchParams();
  const filters = useMemo(() => parseFilters(params), [params]);
  const update = useCallback(
    (patch: Partial<Filters>) => {
      setParams(
        (prev) => {
          const next = new URLSearchParams(prev);
          for (const [k, v] of Object.entries(patch)) {
            const val = Array.isArray(v) ? v.join(",") : (v ?? "");
            if (val) next.set(k, val);
            else next.delete(k);
          }
          return next;
        },
        { replace: true },
      );
    },
    [setParams],
  );
  return [filters, update];
}
