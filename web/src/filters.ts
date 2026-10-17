import { useCallback, useMemo } from "react";
import { useSearchParams } from "react-router-dom";

export interface Filters {
  from?: string;
  to?: string;
  state?: string;
  vru: string[];
  severity?: string;
  hit_and_run?: string;
}

export const VRU_OPTIONS = ["pedestrian", "cyclist", "wheelchair_user", "scooter_rider", "motorcyclist", "other"];
export const SEVERITIES = ["fatal", "serious", "minor", "unknown"];

export function parseFilters(p: URLSearchParams): Filters {
  const vru = (p.get("vru") ?? "").split(",").filter(Boolean);
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
