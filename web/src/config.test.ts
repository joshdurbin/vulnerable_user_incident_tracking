import { defaultConfig, loadConfig, workflowUrl } from "./config";

describe("config", () => {
  it("hides Temporal links when no UI URL is configured", () => {
    expect(workflowUrl(defaultConfig, "incident-1")).toBeNull();
  });
  it("builds a deep link and escapes the workflow id", () => {
    expect(workflowUrl({ temporalUiUrl: "https://t.example/", temporalNamespace: "collisions" }, "incident-a/b")).toBe(
      "https://t.example/namespaces/collisions/workflows/incident-a%2Fb",
    );
  });
  it("falls back to defaults when config.json is missing", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue({ ok: false }));
    expect(await loadConfig()).toEqual(defaultConfig);
    vi.unstubAllGlobals();
  });
});
