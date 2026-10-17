import { LngLatBounds, Map as MapLibreMap, NavigationControl, setWorkerUrl, type GeoJSONSource, type MapGeoJSONFeature } from "maplibre-gl";
// Vite bundles the worker (and the shared chunk it imports) and gives its URL; without this the
// default worker URL does not resolve in a production build.
import workerUrl from "maplibre-gl/dist/maplibre-gl-worker.mjs?worker&url";
import "maplibre-gl/dist/maplibre-gl.css";
import { useEffect, useRef } from "react";
import type { Incident } from "../api/client";
import { toFeatureCollection } from "../geo";
import { SEVERITY_COLOR } from "../format";

setWorkerUrl(workerUrl);

// OpenFreeMap serves free vector tiles without an API key. The browser fetches them
// directly, so the map needs internet access from the device viewing it.
const STYLE = "https://tiles.openfreemap.org/styles/positron";

interface Props {
  items: Incident[];
  selectedId?: string;
  onSelect: (id: string) => void;
}

export default function MapView({ items, selectedId, onSelect }: Props) {
  const el = useRef<HTMLDivElement>(null);
  const map = useRef<MapLibreMap | null>(null);
  const ready = useRef(false);
  const fitted = useRef(false);
  const onSelectRef = useRef(onSelect);
  onSelectRef.current = onSelect;
  const latest = useRef(items);
  latest.current = items;

  useEffect(() => {
    if (!el.current) return;
    const m = new MapLibreMap({ container: el.current, style: STYLE, center: [-119.5, 37.2], zoom: 5, attributionControl: { compact: true } });
    map.current = m;
    m.addControl(new NavigationControl({ showCompass: false }), "top-right");
    m.on("load", () => {
      m.addSource("incidents", { type: "geojson", data: toFeatureCollection(latest.current), cluster: true, clusterRadius: 45, clusterMaxZoom: 13 });
      m.addLayer({
        id: "clusters", type: "circle", source: "incidents", filter: ["has", "point_count"],
        paint: { "circle-color": "#495057", "circle-opacity": 0.85, "circle-radius": ["step", ["get", "point_count"], 14, 10, 18, 50, 24], "circle-stroke-width": 2, "circle-stroke-color": "#fff" },
      });
      m.addLayer({
        id: "cluster-count", type: "symbol", source: "incidents", filter: ["has", "point_count"],
        layout: { "text-field": ["get", "point_count_abbreviated"], "text-size": 12, "text-font": ["Noto Sans Regular"] },
        paint: { "text-color": "#fff" },
      });
      m.addLayer({
        id: "points", type: "circle", source: "incidents", filter: ["!", ["has", "point_count"]],
        paint: {
          "circle-radius": ["case", ["boolean", ["feature-state", "selected"], false], 10, 7],
          "circle-color": ["match", ["get", "severity"], "fatal", SEVERITY_COLOR.fatal, "serious", SEVERITY_COLOR.serious, "minor", SEVERITY_COLOR.minor, SEVERITY_COLOR.unknown],
          "circle-stroke-width": 2, "circle-stroke-color": "#fff",
        },
      });
      m.on("click", "clusters", (e) => {
        const f = e.features?.[0] as MapGeoJSONFeature | undefined;
        if (!f) return;
        const src = m.getSource("incidents") as GeoJSONSource;
        src.getClusterExpansionZoom(f.properties.cluster_id as number).then((zoom) => {
          m.easeTo({ center: (f.geometry as GeoJSON.Point).coordinates as [number, number], zoom });
        });
      });
      m.on("click", "points", (e) => {
        const id = e.features?.[0]?.properties?.id as string | undefined;
        if (id) onSelectRef.current(id);
      });
      for (const layer of ["clusters", "points"]) {
        m.on("mouseenter", layer, () => (m.getCanvas().style.cursor = "pointer"));
        m.on("mouseleave", layer, () => (m.getCanvas().style.cursor = ""));
      }
      ready.current = true;
      push();
    });
    function push() {
      const src = m.getSource("incidents") as GeoJSONSource | undefined;
      if (!src || !ready.current) return;
      const fc = toFeatureCollection(latest.current);
      src.setData(fc);
      if (!fitted.current && fc.features.length > 0) {
        const b = new LngLatBounds();
        fc.features.forEach((f) => b.extend(f.geometry.coordinates as [number, number]));
        m.fitBounds(b, { padding: 60, maxZoom: 12, duration: 0 });
        fitted.current = true;
      }
    }
    (m as unknown as { _push: () => void })._push = push;
    return () => {
      ready.current = false;
      m.remove();
      map.current = null;
    };
  }, []);

  useEffect(() => {
    (map.current as unknown as { _push?: () => void } | null)?._push?.();
  }, [items]);

  // Highlight the selected point and move the view to it.
  useEffect(() => {
    const m = map.current;
    if (!m || !ready.current || !selectedId) return;
    const it = items.find((i) => i.id === selectedId);
    if (it?.lat != null && it.lon != null) m.easeTo({ center: [it.lon, it.lat], zoom: Math.max(m.getZoom(), 13) });
  }, [selectedId, items]);

  return <div ref={el} className="map" role="application" aria-label="Incident map" />;
}
