import { parseFilters, toQuery } from "./filters";

describe("filters", () => {
  it("round-trips the URL", () => {
    const f = parseFilters(new URLSearchParams("from=2026-01-01&state=CA&vru=pedestrian,bicyclist&severity=fatal"));
    expect(f).toMatchObject({ from: "2026-01-01", state: "CA", vru: ["pedestrian", "bicyclist"], severity: "fatal" });
    expect(toQuery(f)).toMatchObject({ vru: ["pedestrian", "bicyclist"], severity: "fatal" });
  });
  it("drops categories the classifier never produces", () => {
    expect(parseFilters(new URLSearchParams("vru=pedestrian,cyclist,nonsense")).vru).toEqual(["pedestrian"]);
  });
  it("drops empty values so they are not sent to the API", () => {
    const q = toQuery(parseFilters(new URLSearchParams("state=&vru=")));
    expect(q.state).toBeUndefined();
    expect(q.vru).toBeUndefined();
  });
});
