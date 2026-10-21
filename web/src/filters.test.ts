import { parseFilters, toListQuery, toQuery } from "./filters";

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

describe("search text", () => {
  it("is read from the URL, trimmed and bounded", () => {
    expect(parseFilters(new URLSearchParams("q=%20telegraph%20ave%20")).q).toBe("telegraph ave");
    expect(parseFilters(new URLSearchParams("q=" + "x".repeat(300))).q).toHaveLength(200);
    expect(parseFilters(new URLSearchParams("q=%20%20")).q).toBeUndefined();
  });
  it("goes to the incident list but not to the statistics", () => {
    const f = parseFilters(new URLSearchParams("q=oakland&state=CA"));
    expect(toListQuery(f)).toMatchObject({ q: "oakland", state: "CA" });
    expect(toQuery(f)).not.toHaveProperty("q");
  });
});
