import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import type { Incident, Review } from "../api/client";
import { adminToken } from "../api/client";
import ReviewsPage, { comparison } from "./ReviewsPage";

const inc = (over: Partial<Incident>): Incident => ({
  id: crypto.randomUUID(), occurred_on: "2026-10-10", occurred_precision: "date", location_precision: "street",
  vru_categories: ["pedestrian"], vehicle_categories: ["passenger_car"], severity: "serious", hit_and_run: "no",
  facts_status: "confirmed", verification_status: "unverified", article_count: 1, updated_at: "2026-10-11T00:00:00Z",
  city: "Oakland", state: "CA", street_address: "100 Broadway", injured_count: 1, ...over,
});

const review = (): Review => ({
  id: "11111111-1111-1111-1111-111111111111", status: "pending", score: 0.62, created_at: "2026-10-11T00:00:00Z",
  new_incident: inc({ occurred_on: undefined, occurred_precision: "unknown" }), candidate_incident: inc({}),
});

function setup(fetchImpl: (req: Request) => Response | Promise<Response>) {
  // The client calls fetch(Request); hand the handler that Request intact (method, headers, body).
  vi.stubGlobal("fetch", vi.fn((req: Request) => Promise.resolve(fetchImpl(req))));
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={qc}>
      <MemoryRouter><ReviewsPage /></MemoryRouter>
    </QueryClientProvider>,
  );
}

afterEach(() => { vi.unstubAllGlobals(); adminToken.set(""); localStorage.clear(); });

describe("comparison", () => {
  it("flags only the fields that differ", () => {
    const rows = comparison(review().new_incident, review().candidate_incident);
    expect(rows.find((r) => r.field === "When")?.differs).toBe(true);
    expect(rows.find((r) => r.field === "Road user")?.differs).toBe(false);
  });
});

describe("ReviewsPage", () => {
  it("requires a name before a decision can be made", async () => {
    setup(() => Response.json([review()]));
    const merge = await screen.findByRole("button", { name: /merge/i });
    expect(merge).toBeDisabled();
    await userEvent.type(screen.getByLabelText(/your name/i), "josh");
    expect(merge).toBeEnabled();
  });

  it("sends the decision with the admin token and refreshes the queue", async () => {
    adminToken.set("s3cret");
    let queue = [review()];
    let seen: { auth: string | null; body: unknown } | undefined;
    setup(async (req) => {
      if (req.method === "POST") {
        seen = { auth: req.headers.get("authorization"), body: await req.clone().json() };
        queue = [];
        return Response.json({ review_id: review().id, approved: true });
      }
      return Response.json(queue);
    });
    await userEvent.type(await screen.findByLabelText(/your name/i), "josh");
    await userEvent.click(screen.getByRole("button", { name: /merge/i }));
    await waitFor(() => expect(screen.getByText(/nothing to review/i)).toBeInTheDocument());
    expect(seen?.auth).toBe("Bearer s3cret");
    expect(seen?.body).toEqual({ approve: true, actor: "josh" });
  });

  it("shows the API's message when the server refuses", async () => {
    setup((req) => (req.method === "POST" ? Response.json({ error: "missing or wrong admin token" }, { status: 401 }) : Response.json([review()])));
    await userEvent.type(await screen.findByLabelText(/your name/i), "josh");
    await userEvent.click(screen.getByRole("button", { name: /keep separate/i }));
    expect(await screen.findByRole("alert")).toHaveTextContent(/admin token/i);
  });
});
