import { casualties, fillCalendar, MONTHS, WEEKDAYS, when, where } from "./format";

describe("format", () => {
  it("formats a calendar date without shifting it across time zones", () => {
    // 2026-10-01 must stay Oct 1 whatever the viewer's zone is.
    expect(when({ occurred_on: "2026-10-01", occurred_at: null, occurred_precision: "date" })).toContain("1");
    expect(when({ occurred_on: "2026-10-01", occurred_at: null, occurred_precision: "date" })).toMatch(/Oct/);
  });
  it("says so when the date is unknown", () => {
    expect(when({ occurred_on: undefined, occurred_at: undefined, occurred_precision: "unknown" })).toBe("Date unknown");
  });
  it("does not invent a location", () => {
    expect(where({})).toBe("Location unknown");
    expect(where({ city: "Oakland", state: "CA" })).toBe("Oakland, CA");
  });
  it("keeps unknown counts distinct from zero", () => {
    expect(casualties({})).toBe("Outcome not reported");
    expect(casualties({ fatality_count: 0, injured_count: 2 })).toBe("0 killed, 2 injured");
  });
  it("fills weekdays and months that have no incidents", () => {
    const wk = fillCalendar([{ key: "1", incidents: 4 }, { key: "7", incidents: 2 }], WEEKDAYS, false);
    expect(wk).toHaveLength(7);
    expect(wk[0]).toEqual({ name: "Mon", incidents: 4 });
    expect(wk[2].incidents).toBe(0);
    expect(wk[6]).toEqual({ name: "Sun", incidents: 2 });
    expect(fillCalendar([{ key: "12", incidents: 1 }], MONTHS, false)[11]).toEqual({ name: "Dec", incidents: 1 });
  });
});
