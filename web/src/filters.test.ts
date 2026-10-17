import { parseFilters, toQuery } from "./filters";

describe("filters", () => {
  it("round-trips the URL", () => {
    const f = parseFilters(new URLSearchParams("from=2026-01-01&state=CA&vru=pedestrian,cyclist&severity=fatal"));
    expect(f).toMatchObject({ from: "2026-01-01", state: "CA", vru: ["pedestrian", "cyclist"], severity: "fatal" });
    expect(toQuery(f)).toMatchObject({ vru: ["pedestrian", "cyclist"], severity: "fatal" });
  });
  it("drops empty values so they are not sent to the API", () => {
    const q = toQuery(parseFilters(new URLSearchParams("state=&vru=")));
    expect(q.state).toBeUndefined();
    expect(q.vru).toBeUndefined();
  });
});
