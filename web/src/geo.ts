import type { Incident } from "./api/client";

/** Only incidents with coordinates can be drawn; the rest stay in the list. */
export function toFeatureCollection(items: Incident[]): GeoJSON.FeatureCollection<GeoJSON.Point> {
  return {
    type: "FeatureCollection",
    features: items
      .filter((i) => i.lat != null && i.lon != null)
      .map((i) => ({
        type: "Feature" as const,
        geometry: { type: "Point" as const, coordinates: [i.lon as number, i.lat as number] },
        properties: { id: i.id, severity: i.severity },
      })),
  };
}
