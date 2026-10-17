import type { Incident } from "./api/client";
import { toFeatureCollection } from "./geo";

const base = { id: "x", severity: "fatal" } as Incident;

describe("toFeatureCollection", () => {
  it("skips incidents without coordinates instead of placing them at 0,0", () => {
    const fc = toFeatureCollection([
      { ...base, id: "a", lat: 37.8, lon: -122.27 },
      { ...base, id: "b" },
      { ...base, id: "c", lat: 0, lon: 0 },
    ]);
    expect(fc.features.map((f) => f.properties?.id)).toEqual(["a", "c"]);
    expect(fc.features[0].geometry.coordinates).toEqual([-122.27, 37.8]); // GeoJSON is [lon, lat]
  });
});
