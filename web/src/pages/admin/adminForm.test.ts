import type { SourceDetail, SourceType } from "../../api/client";
import { ago, csv, typeLabel, until, emptyForm, formFromDetail, healthSummary, inputFromForm, intervalLabel, lines, needsAttention, percent, quickProblems, seconds } from "./adminForm";

const crawlNone = { allowed_domains: false, require_domains: false, url_patterns: false, max_pages: false, max_depth: false, render: false };
const rss: SourceType = { type: "rss", label: "RSS feed", description: "", url: "required", uses_queries: false, uses_coverage: false, crawl: crawlNone };
const gdelt: SourceType = { type: "gdelt", label: "GDELT", description: "", url: "none", uses_queries: true, uses_coverage: true, crawl: crawlNone };
const index: SourceType = {
  type: "index_page", label: "Index page", description: "", url: "required", uses_queries: false, uses_coverage: false,
  crawl: { allowed_domains: true, require_domains: true, url_patterns: true, max_pages: true, max_depth: true, render: true },
};

describe("splitting", () => {
  it("drops blanks and trims", () => {
    expect(csv(" CA, ,OR ,")).toEqual(["CA", "OR"]);
    expect(lines("a.gov\n\n  b.gov  \n")).toEqual(["a.gov", "b.gov"]);
  });
});

describe("inputFromForm", () => {
  const base = { ...emptyForm("rss"), slug: " feed-a ", name: " Feed A ", url: " https://a.test/feed ", pollMinutes: "45" };

  it("trims, converts minutes to seconds and sends the url only for url types", () => {
    expect(inputFromForm(base, rss)).toEqual({ slug: "feed-a", name: "Feed A", type: "rss", url: "https://a.test/feed", poll_interval_seconds: 2700, enabled: true });
    const search = inputFromForm({ ...base, type: "gdelt" }, gdelt);
    expect(search.url).toBeUndefined();
  });

  it("sends coverage only when something was entered", () => {
    expect(inputFromForm(base, rss).coverage).toBeUndefined();
    expect(inputFromForm({ ...base, country: "US", states: "CA, OR" }, rss).coverage).toEqual({ country: "US", states: ["CA", "OR"], cities: undefined });
  });

  it("sends crawl settings only for fields the type uses, so switching type cannot leave stale ones behind", () => {
    const crawly = { ...base, type: "index_page", allowedDomains: "pd.gov", urlPatterns: "/news/", maxPages: "20", maxDepth: "2", render: "auto" };
    expect(inputFromForm(crawly, index).crawl).toEqual({ allowed_domains: ["pd.gov"], url_patterns: ["/news/"], max_pages: 20, max_depth: 2, render: "auto" });
    expect(inputFromForm(crawly, rss).crawl, "an RSS feed has no crawl settings").toBeUndefined();
  });

  it("ignores non-positive and non-numeric crawl limits", () => {
    const f = { ...emptyForm("index_page"), slug: "x", name: "x", url: "https://x.test", allowedDomains: "x.test", maxPages: "0", maxDepth: "abc" };
    expect(inputFromForm(f, index).crawl).toEqual({ allowed_domains: ["x.test"] });
  });
});

describe("formFromDetail", () => {
  it("round-trips a saved source back into the same API input", () => {
    const d = {
      slug: "s", name: "S", type: "index_page", url: "https://x.test/news", enabled: false, poll_interval_seconds: 3600,
      coverage: { country: "US", states: ["CA"], cities: ["Oakland", "Berkeley"] },
      crawl: { allowed_domains: ["x.test"], url_patterns: ["/n/"], max_pages: 5, render: "auto" },
    } as SourceDetail;
    expect(inputFromForm(formFromDetail(d), index)).toEqual({
      slug: "s", name: "S", type: "index_page", url: "https://x.test/news", enabled: false, poll_interval_seconds: 3600,
      coverage: { country: "US", states: ["CA"], cities: ["Oakland", "Berkeley"] },
      crawl: { allowed_domains: ["x.test"], url_patterns: ["/n/"], max_pages: 5, render: "auto" },
    });
  });
});

describe("quickProblems", () => {
  it("lists what is missing, and nothing for a complete form", () => {
    expect(quickProblems(emptyForm(), undefined, true)).toHaveLength(3); // slug, name, type
    const ok = { ...emptyForm("rss"), slug: "a", name: "A", url: "https://a.test" };
    expect(quickProblems(ok, rss, true)).toEqual([]);
    expect(quickProblems({ ...ok, url: "" }, rss, true)).toEqual(["This type needs a URL."]);
    expect(quickProblems({ ...ok, slug: "" }, rss, false), "an existing source's slug is fixed").toEqual([]);
    expect(quickProblems({ ...ok, pollMinutes: "0" }, rss, true)[0]).toMatch(/at least 1 minute/);
  });

  it("requires allowed domains for a crawled type", () => {
    const f = { ...emptyForm("index_page"), slug: "a", name: "A", url: "https://a.test" };
    expect(quickProblems(f, index, true)).toEqual(["This type needs at least one allowed domain."]);
    expect(quickProblems({ ...f, allowedDomains: "a.test" }, index, true)).toEqual([]);
  });
});

describe("health", () => {
  it("counts sources by state and knows which need a person", () => {
    const s = healthSummary([{ health: { state: "healthy" } }, { health: { state: "healthy" } }, { health: { state: "failing" } }]);
    expect(s.healthy).toBe(2);
    expect(s.failing).toBe(1);
    expect(s.paused).toBe(0);
    expect(["failing", "stale", "degraded"].every((x) => needsAttention(x as never))).toBe(true);
    expect(["healthy", "paused", "disabled", "waiting"].some((x) => needsAttention(x as never))).toBe(false);
  });
});

describe("formatting", () => {
  it("labels intervals in the largest exact unit", () => {
    expect(intervalLabel(1800)).toBe("30 min");
    expect(intervalLabel(3600)).toBe("1 h");
    expect(intervalLabel(86400)).toBe("1 d");
    expect(intervalLabel(90)).toBe("90 s");
  });
  it("says how long ago", () => {
    const now = Date.parse("2026-10-18T12:00:00Z");
    expect(ago(null, now)).toBe("never");
    expect(ago("2026-10-18T11:59:30Z", now)).toBe("just now");
    expect(ago("2026-10-18T11:30:00Z", now)).toBe("30 min ago");
    expect(ago("2026-10-18T07:00:00Z", now)).toBe("5 h ago");
    expect(ago("2026-10-15T12:00:00Z", now)).toBe("3 d ago");
  });
  it("formats rates and durations", () => {
    expect(percent(0.756)).toBe("76%");
    expect(percent(undefined)).toBe("–");
    expect(seconds(1.234)).toBe("1.2 s");
    expect(seconds(42.4)).toBe("42 s");
  });
});

describe("until and typeLabel", () => {
  const now = Date.parse("2026-10-18T12:00:00Z");
  it("describes a time ahead", () => {
    expect(until(null, now)).toBe("–");
    expect(until("2026-10-18T11:59:00Z", now)).toBe("overdue");
    expect(until("2026-10-18T12:00:30Z", now)).toBe("in under a minute");
    expect(until("2026-10-18T12:15:00Z", now)).toBe("in 15 min");
    expect(until("2026-10-18T15:00:00Z", now)).toBe("in 3 h");
  });
  it("uses the provider's label and falls back to the raw type", () => {
    expect(typeLabel([{ type: "rss", label: "RSS feed" }], "rss")).toBe("RSS feed");
    expect(typeLabel([], "future_type")).toBe("future_type");
  });
});
