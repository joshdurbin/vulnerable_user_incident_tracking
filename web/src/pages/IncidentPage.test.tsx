import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import App from "../App";
import { RuntimeConfigProvider } from "../RuntimeConfig";
import { actorName } from "../actor";
import { adminToken } from "../api/client";

const incident = (articles: number) => ({
  id: "inc-1", occurred_precision: "date", location_precision: "street", vru_categories: ["pedestrian"], vehicle_categories: [],
  severity: "fatal", hit_and_run: "no", facts_status: "confirmed", verification_status: articles >= 2 ? "corroborated" : "unverified",
  article_count: articles, updated_at: "2026-10-18T10:00:00Z", city: "Oakland", state: "CA",
});
const article = (id: string, title: string, over = {}) => ({ article_id: id, title, publisher: `${id}.example`, url: `https://${id}.example/x`, match_status: "created", matched_by: "rules", ...over });
const detail = (arts: ReturnType<typeof article>[], count: number) => ({ incident: incident(count), workflow_id: "incident-inc-1", articles: arts, observations: [], events: [] });

function renderIncident(d: ReturnType<typeof detail>, split?: (body: unknown) => Response) {
  const posts: unknown[] = [];
  vi.stubGlobal("fetch", vi.fn(async (req: Request) => {
    const path = new URL(req.url).pathname;
    if (path === "/v1/incidents/inc-1") return Response.json(d);
    if (path === "/v1/admin/incidents/inc-1/split" && req.method === "POST") {
      const body = await req.json();
      posts.push({ body, auth: req.headers.get("Authorization") });
      return split ? split(body) : Response.json({ new_incident_id: "inc-2" });
    }
    if (path === "/v1/ops/pipeline") return Response.json({ counts: { discovered_urls: 0, articles: 0, failed_articles: 0, extractions: 0, pending_reviews: 0 } });
    throw new Error(`unhandled ${req.method} ${path}`);
  }));
  render(
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
      <RuntimeConfigProvider><MemoryRouter initialEntries={["/incidents/inc-1"]}><App /></MemoryRouter></RuntimeConfigProvider>
    </QueryClientProvider>,
  );
  return posts;
}

afterEach(() => { vi.unstubAllGlobals(); adminToken.set(""); localStorage.clear(); });

describe("Incident page: sources and splitting", () => {
  const two = detail([article("a", "First report"), article("b", "Second report")], 2);

  it("offers no split to someone without the admin token", async () => {
    renderIncident(two);
    expect(await screen.findByText("First report")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /move to a new incident/i })).toBeNull();
  });

  it("offers no split when there is only one independent source, and says copies count once", async () => {
    adminToken.set("tok");
    renderIncident(detail([article("a", "Original"), article("b", "Reprint", { duplicate_of: "a", duplicate_kind: "near_duplicate" })], 1));
    expect(await screen.findByText("Reprint")).toBeInTheDocument();
    expect(screen.getByText(/2 articles; copies of the same story count once/)).toBeInTheDocument();
    expect(screen.getByText(/republished copy of an earlier source/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /move to a new incident/i })).toBeNull();
  });

  it("moves an article to a new incident with the admin token and the person's name", async () => {
    adminToken.set("tok");
    actorName.set("Pat");
    const confirm = vi.spyOn(window, "confirm").mockReturnValueOnce(false).mockReturnValueOnce(true);
    const posts = renderIncident(two);
    const li = (await screen.findByText("Second report")).closest("li") as HTMLElement;
    const button = within(li).getByRole("button", { name: /move to a new incident/i });

    await userEvent.click(button); // declined
    expect(posts).toHaveLength(0);
    await userEvent.click(button);
    await waitFor(() => expect(posts).toHaveLength(1));
    expect(posts[0]).toEqual({ body: { article_id: "b", actor: "Pat" }, auth: "Bearer tok" });
    expect(await screen.findByRole("link", { name: /open the new incident/i })).toHaveAttribute("href", "/incidents/inc-2");
    confirm.mockRestore();
  });

  it("shows why the server refused", async () => {
    adminToken.set("tok");
    actorName.set("Pat");
    vi.spyOn(window, "confirm").mockReturnValue(true);
    renderIncident(two, () => Response.json({ error: "cannot split: incident has fewer than two articles" }, { status: 409 }));
    const li = (await screen.findByText("First report")).closest("li") as HTMLElement;
    await userEvent.click(within(li).getByRole("button", { name: /move to a new incident/i }));
    expect(await screen.findByText(/fewer than two articles/)).toBeInTheDocument();
  });
});
