import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import App from "../../App";
import { adminToken, type Query, type SourceDetail, type SourceType } from "../../api/client";

const crawlNone = { allowed_domains: false, require_domains: false, url_patterns: false, max_pages: false, max_depth: false, render: false };
const TYPES: SourceType[] = [
  { type: "rss", label: "RSS feed", description: "Reads one feed.", url: "required", url_hint: "https://example.org/feed/", uses_queries: false, uses_coverage: false, crawl: crawlNone },
  { type: "gdelt", label: "GDELT", description: "Searches GDELT.", url: "none", uses_queries: true, uses_coverage: true, crawl: crawlNone },
  {
    type: "index_page", label: "Index page", description: "Crawls an index.", url: "required", uses_queries: false, uses_coverage: false,
    crawl: { allowed_domains: true, require_domains: true, url_patterns: true, max_pages: true, max_depth: true, render: true },
  },
];

const source = (over: Partial<SourceDetail> = {}): SourceDetail => ({
  slug: "oaklandside", name: "Oaklandside", type: "rss", url: "https://oaklandside.org/feed/", enabled: true, poll_interval_seconds: 1800,
  health: { state: "healthy", reason: "running normally" }, consecutive_failures: 0, last_success_at: new Date(Date.now() - 5 * 60_000).toISOString(),
  schedule: { paused: false, next_run: "2026-10-18T13:00:00Z" },
  run_stats: { runs: 10, successes: 9, partials: 0, failures: 1, success_rate: 0.9, avg_seconds: 2.5, p95_seconds: 4, new_urls: 31, requests: 10 },
  quality: { discoveries: 40, unique_urls: 31, exclusive_urls: 12, articles_fetched: 20, fetch_success_rate: 0.95, relevant_rate: 0.4, incidents: 6, avg_delay_seconds: 900 },
  ...over,
});

type Handler = (req: Request, path: string) => Response | undefined | Promise<Response | undefined>;

/** Render the whole app at a path with a stubbed network. Unhandled requests fail the test loudly. */
function renderAt(path: string, handler: Handler) {
  const calls: { method: string; path: string; auth: string | null; body: unknown }[] = [];
  vi.stubGlobal("fetch", vi.fn(async (req: Request) => {
    const url = new URL(req.url);
    const text = req.method === "GET" || req.method === "DELETE" ? "" : await req.clone().text();
    calls.push({ method: req.method, path: url.pathname + url.search, auth: req.headers.get("Authorization"), body: text ? JSON.parse(text) : undefined });
    const res = await handler(req, url.pathname);
    if (res) return res;
    if (url.pathname === "/v1/ops/pipeline") return Response.json({ counts: { discovered_urls: 1200, articles: 800, failed_articles: 12, extractions: 700, pending_reviews: 3 } });
    if (url.pathname === "/v1/admin/source-types") return Response.json(TYPES);
    throw new Error(`unhandled ${req.method} ${url.pathname}`);
  }));
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={qc}>
      <MemoryRouter initialEntries={[path]}><App /></MemoryRouter>
    </QueryClientProvider>,
  );
  return calls;
}

beforeEach(() => adminToken.set("tok"));
afterEach(() => { vi.unstubAllGlobals(); adminToken.set(""); localStorage.clear(); });

describe("the admin token gate", () => {
  it("asks for the token before showing anything, and sends it with every admin request", async () => {
    adminToken.set("");
    const calls = renderAt("/admin/sources", (_r, p) => (p === "/v1/admin/sources" ? Response.json([source()]) : undefined));
    expect(await screen.findByRole("heading", { name: /admin token/i })).toBeInTheDocument();
    expect(calls.filter((c) => c.path.startsWith("/v1/admin"))).toHaveLength(0);

    await userEvent.type(screen.getByLabelText(/token/i), "s3cret");
    await userEvent.click(screen.getByRole("button", { name: /continue/i }));
    expect(await screen.findByText("Oaklandside")).toBeInTheDocument();
    const admin = calls.filter((c) => c.path.startsWith("/v1/admin"));
    expect(admin.length).toBeGreaterThan(0);
    expect(admin.every((c) => c.auth === "Bearer s3cret")).toBe(true);
    expect(adminToken.get()).toBe("s3cret");
  });

  it("says so when the token is wrong", async () => {
    adminToken.set("nope");
    renderAt("/admin", (_r, p) => (p === "/v1/admin/source-types" ? Response.json({ error: "missing or wrong admin token" }, { status: 401 }) : undefined));
    expect(await screen.findByText(/token was not accepted/i)).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: /admin token/i })).toBeInTheDocument();
  });
});

describe("Overview", () => {
  it("summarises health and points at what needs attention", async () => {
    renderAt("/admin", (_r, p) => p === "/v1/admin/sources" ? Response.json([
      source(),
      source({ slug: "bad", name: "Broken Feed", health: { state: "failing", reason: "3 runs in a row failed: http 503" } }),
    ]) : undefined);
    expect(await screen.findByText("1,200")).toBeInTheDocument();
    const link = await screen.findByRole("link", { name: "Broken Feed" });
    expect(link).toHaveAttribute("href", "/admin/sources/bad");
    expect(screen.getByText(/3 runs in a row failed/)).toBeInTheDocument();
    expect(within(screen.getByLabelText("Sources by health")).getAllByRole("listitem")).toHaveLength(2);
  });

  it("explains what to do when no sources are configured", async () => {
    renderAt("/admin", (_r, p) => (p === "/v1/admin/sources" ? Response.json([]) : undefined));
    expect(await screen.findByText(/nothing is being collected/i)).toBeInTheDocument();
    expect(screen.getByRole("link", { name: /add a source/i })).toHaveAttribute("href", "/admin/sources/new");
  });
});

describe("Sources list", () => {
  it("shows health, run statistics and quality, and pauses a source through the schedule route", async () => {
    const calls = renderAt("/admin/sources", (r, p) => {
      if (p === "/v1/admin/sources") return Response.json([source()]);
      if (p === "/v1/admin/sources/oaklandside/schedule" && r.method === "POST") return new Response(null, { status: 204 });
    });
    const row = (await screen.findByText("Oaklandside")).closest("tr") as HTMLElement;
    expect(within(row).getByText("Healthy")).toBeInTheDocument();
    expect(within(row).getByText(/RSS feed · every 30 min/)).toBeInTheDocument();
    expect(within(row).getByText("90%")).toBeInTheDocument();
    expect(within(row).getByText("2.5 s")).toBeInTheDocument();
    expect(within(row).getByText("40%")).toBeInTheDocument();

    await userEvent.click(within(row).getByRole("button", { name: "Pause" }));
    await waitFor(() => expect(calls.some((c) => c.path === "/v1/admin/sources/oaklandside/schedule")).toBe(true));
    const post = calls.find((c) => c.path === "/v1/admin/sources/oaklandside/schedule");
    expect(post?.body).toEqual({ action: "pause" });
    expect(post?.auth).toBe("Bearer tok");
  });

  it("offers Resume for a paused schedule and says a source without a schedule has none", async () => {
    renderAt("/admin/sources", (_r, p) => p === "/v1/admin/sources" ? Response.json([
      source({ slug: "a", name: "A", schedule: { paused: true }, health: { state: "paused", reason: "schedule is paused" } }),
      source({ slug: "b", name: "B", schedule: undefined }),
    ]) : undefined);
    const a = (await screen.findByText("A")).closest("tr") as HTMLElement;
    expect(within(a).getByRole("button", { name: "Resume" })).toBeInTheDocument();
    expect(within(a).getByText("Paused")).toBeInTheDocument();
    const b = screen.getByText("B").closest("tr") as HTMLElement;
    expect(within(b).getByText(/no schedule/i)).toBeInTheDocument();
  });

  it("syncs schedules and reports what changed", async () => {
    renderAt("/admin/sources", (_r, p) => {
      if (p === "/v1/admin/sources") return Response.json([source()]);
      if (p === "/v1/admin/schedules/sync") return Response.json({ created: 1, updated: 2, removed: 3 });
    });
    await userEvent.click(await screen.findByRole("button", { name: /sync schedules/i }));
    expect(await screen.findByText(/1 created, 2 updated, 3 removed/)).toBeInTheDocument();
  });
});

describe("Source detail", () => {
  it("shows the run history with errors, and deletes only after confirmation", async () => {
    vi.spyOn(window, "confirm").mockReturnValueOnce(false).mockReturnValueOnce(true);
    const calls = renderAt("/admin/sources/oaklandside", (r, p) => {
      if (p === "/v1/admin/sources/oaklandside" && r.method === "GET") return Response.json(source());
      if (p === "/v1/admin/sources/oaklandside/runs") return Response.json([
        { started_at: new Date(Date.now() - 60_000).toISOString(), finished_at: new Date(Date.now() - 57_000).toISOString(), status: "failed", found: 0, unique_urls: 0, new_urls: 0, known_urls: 0, pending_urls: 0, unresolved: 0, requests: 1, truncated: false, warnings: [], error: "feed oaklandside: http 503" },
        { started_at: new Date(Date.now() - 1_800_000).toISOString(), finished_at: new Date(Date.now() - 1_797_000).toISOString(), status: "success", found: 20, unique_urls: 18, new_urls: 4, known_urls: 14, pending_urls: 0, unresolved: 0, requests: 1, truncated: true, warnings: ["slow"] },
      ]);
      if (p === "/v1/admin/sources/oaklandside" && r.method === "DELETE") return new Response(null, { status: 204 });
      if (p === "/v1/admin/sources") return Response.json([]);
    });
    expect(await screen.findByText("feed oaklandside: http 503")).toBeInTheDocument();
    expect(screen.getByText(/results were cut off · slow/)).toBeInTheDocument();
    expect(screen.getByText("Failed")).toBeInTheDocument();
    expect(screen.getByText("OK")).toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: "Delete" }));
    expect(calls.some((c) => c.method === "DELETE")).toBe(false);
    await userEvent.click(screen.getByRole("button", { name: "Delete" }));
    await waitFor(() => expect(calls.some((c) => c.method === "DELETE")).toBe(true));
  });
});

describe("Source form", () => {
  const filled = async () => {
    await userEvent.selectOptions(await screen.findByLabelText(/^type/i), "rss");
    await userEvent.type(screen.getByLabelText(/^name/i), "Test Feed");
    await userEvent.type(screen.getByLabelText(/^slug/i), "test-feed");
    await userEvent.type(screen.getByLabelText(/^url/i), "https://t.test/feed");
  };

  it("shows only the fields the chosen type uses", async () => {
    renderAt("/admin/sources/new", () => undefined);
    const type = await screen.findByLabelText(/^type/i);
    expect(screen.queryByLabelText(/^url/i)).not.toBeInTheDocument();

    await userEvent.selectOptions(type, "rss");
    expect(screen.getByLabelText(/^url/i)).toBeInTheDocument();
    expect(screen.queryByLabelText(/^allowed domains/i)).not.toBeInTheDocument();
    expect(screen.getByText("Reads one feed.")).toBeInTheDocument();

    await userEvent.selectOptions(type, "index_page");
    expect(screen.getByLabelText(/^allowed domains/i)).toBeInTheDocument();
    expect(screen.getByLabelText(/javascript rendering/i)).toBeInTheDocument();

    await userEvent.selectOptions(type, "gdelt");
    expect(screen.queryByLabelText(/^url/i)).not.toBeInTheDocument();
    expect(screen.queryByLabelText(/^allowed domains/i)).not.toBeInTheDocument();
  });

  it("cannot be saved until what is missing is filled in, and says what is missing", async () => {
    renderAt("/admin/sources/new", () => undefined);
    const save = await screen.findByRole("button", { name: "Add source" });
    expect(save).toBeDisabled();
    expect(screen.getByLabelText("Still needed")).toHaveTextContent(/slug/i);
    await filled();
    expect(save).toBeEnabled();
    expect(screen.queryByLabelText("Still needed")).not.toBeInTheDocument();
  });

  it("creates the source and goes to its page", async () => {
    const calls = renderAt("/admin/sources/new", (r, p) => {
      if (p === "/v1/admin/sources" && r.method === "POST") return Response.json(source({ slug: "test-feed", name: "Test Feed" }), { status: 201 });
      if (p === "/v1/admin/sources/test-feed") return Response.json(source({ slug: "test-feed", name: "Test Feed" }));
      if (p === "/v1/admin/sources/test-feed/runs") return Response.json([]);
    });
    await filled();
    await userEvent.click(screen.getByRole("button", { name: "Add source" }));
    expect(await screen.findByRole("heading", { name: /Test Feed/ })).toBeInTheDocument();
    const post = calls.find((c) => c.method === "POST" && c.path === "/v1/admin/sources");
    expect(post?.body).toEqual({ slug: "test-feed", name: "Test Feed", type: "rss", url: "https://t.test/feed", poll_interval_seconds: 1800, enabled: true });
    expect(post?.auth).toBe("Bearer tok");
  });

  it("shows every problem the server found", async () => {
    renderAt("/admin/sources/new", (_r, p) => p === "/v1/admin/sources" ? Response.json({
      error: "source is invalid", problems: ["slug must be 2-63 characters", "type rss needs an absolute http(s) url"],
    }, { status: 400 }) : undefined);
    await filled();
    await userEvent.click(screen.getByRole("button", { name: "Add source" }));
    const alert = await screen.findByRole("alert");
    expect(within(alert).getByText("slug must be 2-63 characters")).toBeInTheDocument();
    expect(within(alert).getByText("type rss needs an absolute http(s) url")).toBeInTheDocument();
  });

  it("stays on the form and says so when the source saved but its schedule did not", async () => {
    renderAt("/admin/sources/new", (_r, p) => p === "/v1/admin/sources"
      ? Response.json(source({ slug: "test-feed", schedule_error: "saved, but its schedule could not be updated (temporal is not available)" }), { status: 201 }) : undefined);
    await filled();
    await userEvent.click(screen.getByRole("button", { name: "Add source" }));
    expect(await screen.findByText(/schedule could not be updated/)).toBeInTheDocument();
    expect(screen.getByRole("link", { name: /open source/i })).toHaveAttribute("href", "/admin/sources/test-feed");
  });

  it("tests the configuration without saving, and shows what it found", async () => {
    const calls = renderAt("/admin/sources/new", (_r, p) => p === "/v1/admin/source-tests" ? Response.json({
      ok: true, found: 12, unique_urls: 10, unresolved: 0, requests: 1, truncated: false, duration_ms: 840, warnings: ["only the first 2 of 10 queries were run"],
      samples: [{ url: "https://t.test/a", title: "Pedestrian struck on Broadway", published_at: "2026-10-18T09:00:00Z" }],
    }) : undefined);
    await filled();
    await userEvent.click(screen.getByRole("button", { name: "Test" }));
    const result = await screen.findByLabelText("Test result");
    expect(within(result).getByText(/12 found, 10 unique, in 0.8 s/)).toBeInTheDocument();
    expect(within(result).getByRole("link", { name: "Pedestrian struck on Broadway" })).toHaveAttribute("href", "https://t.test/a");
    expect(within(result).getByText(/only the first 2 of 10 queries/)).toBeInTheDocument();
    expect(calls.some((c) => c.method === "POST" && c.path === "/v1/admin/sources")).toBe(false);
  });

  it("shows a failed test as a failure the person can read", async () => {
    renderAt("/admin/sources/new", (_r, p) => p === "/v1/admin/source-tests"
      ? Response.json({ ok: false, error: "feed t: http 503", found: 0, unique_urls: 0, unresolved: 0, requests: 1, truncated: false, duration_ms: 90, warnings: [], samples: [] }) : undefined);
    await filled();
    await userEvent.click(screen.getByRole("button", { name: "Test" }));
    expect(await screen.findByText(/Failed: feed t: http 503/)).toBeInTheDocument();
  });

  it("edits an existing source, keeping its slug fixed", async () => {
    const calls = renderAt("/admin/sources/oaklandside/edit", (r, p) => {
      if (p === "/v1/admin/sources/oaklandside" && r.method === "GET") return Response.json(source());
      if (p === "/v1/admin/sources/oaklandside" && r.method === "PUT") return Response.json(source({ name: "Renamed" }));
      if (p === "/v1/admin/sources/oaklandside/runs") return Response.json([]);
    });
    const slug = await screen.findByLabelText(/^slug/i);
    expect(slug).toBeDisabled();
    expect(slug).toHaveValue("oaklandside");
    const name = screen.getByLabelText(/^name/i);
    await userEvent.clear(name);
    await userEvent.type(name, "Renamed");
    await userEvent.click(screen.getByRole("button", { name: "Save changes" }));
    await waitFor(() => expect(calls.some((c) => c.method === "PUT")).toBe(true));
    expect(calls.find((c) => c.method === "PUT")?.body).toMatchObject({ slug: "oaklandside", name: "Renamed", poll_interval_seconds: 1800 });
  });
});

describe("Queries", () => {
  const q = (over: Partial<Query> = {}): Query => ({ name: "ped-struck", family: "pedestrian", text: "(pedestrian OR walker) struck", providers: [], enabled: true, ...over });

  it("lists queries and says which sources run them", async () => {
    renderAt("/admin/queries", (_r, p) => p === "/v1/admin/queries" ? Response.json([q(), q({ name: "g-only", providers: ["gdelt"] })]) : undefined);
    expect(await screen.findByText("ped-struck")).toBeInTheDocument();
    expect(screen.getByText("All search sources")).toBeInTheDocument();
    const row = screen.getByText("g-only").closest("tr") as HTMLElement;
    expect(within(row).getByText("GDELT"), "the provider's own label, not the raw type").toBeInTheDocument();
  });

  it("adds a query for the chosen search types only", async () => {
    const calls = renderAt("/admin/queries", (r, p) => {
      if (p === "/v1/admin/queries" && r.method === "GET") return Response.json([]);
      if (p === "/v1/admin/queries" && r.method === "POST") return Response.json(q(), { status: 201 });
    });
    const form = await screen.findByRole("form", { name: "Add query" });
    expect(within(form).queryByLabelText("RSS feed"), "feeds do not run queries").not.toBeInTheDocument();
    await userEvent.type(within(form).getByLabelText(/^name/i), "ped-struck");
    await userEvent.type(within(form).getByLabelText(/^family/i), "pedestrian");
    await userEvent.type(within(form).getByLabelText(/^query/i), "(a OR b) c");
    await userEvent.click(within(form).getByLabelText("GDELT"));
    await userEvent.click(within(form).getByRole("button", { name: "Add query" }));
    await waitFor(() => expect(calls.some((c) => c.method === "POST")).toBe(true));
    expect(calls.find((c) => c.method === "POST")?.body).toEqual({ name: "ped-struck", family: "pedestrian", text: "(a OR b) c", providers: ["gdelt"], enabled: true });
  });

  it("shows the server's reason when a query is rejected", async () => {
    renderAt("/admin/queries", (r, p) => {
      if (p === "/v1/admin/queries" && r.method === "GET") return Response.json([]);
      if (p === "/v1/admin/queries" && r.method === "POST") return Response.json({ error: "text: OR must be inside parentheses" }, { status: 400 });
    });
    const form = await screen.findByRole("form", { name: "Add query" });
    await userEvent.type(within(form).getByLabelText(/^name/i), "bad-or");
    await userEvent.type(within(form).getByLabelText(/^family/i), "p");
    await userEvent.type(within(form).getByLabelText(/^query/i), "a OR b");
    await userEvent.click(within(form).getByRole("button", { name: "Add query" }));
    expect(await screen.findByText("text: OR must be inside parentheses")).toBeInTheDocument();
  });

  it("disables a query with its checkbox", async () => {
    const calls = renderAt("/admin/queries", (r, p) => {
      if (p === "/v1/admin/queries" && r.method === "GET") return Response.json([q()]);
      if (p === "/v1/admin/queries/ped-struck" && r.method === "PUT") return Response.json(q({ enabled: false }));
    });
    await userEvent.click(await screen.findByLabelText("ped-struck enabled"));
    await waitFor(() => expect(calls.some((c) => c.method === "PUT")).toBe(true));
    expect(calls.find((c) => c.method === "PUT")?.body).toMatchObject({ name: "ped-struck", enabled: false });
  });
});
