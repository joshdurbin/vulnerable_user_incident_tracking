export interface RuntimeConfig {
  /** Base URL of the Temporal UI, e.g. https://temporal.example.ts.net. Empty hides the deep links. */
  temporalUiUrl: string;
  temporalNamespace: string;
}

export const defaultConfig: RuntimeConfig = { temporalUiUrl: "", temporalNamespace: "collisions" };

/** Read /config.json, which the container writes from its environment at start. */
export async function loadConfig(): Promise<RuntimeConfig> {
  try {
    const r = await fetch("/config.json", { cache: "no-store" });
    if (!r.ok) return defaultConfig;
    return { ...defaultConfig, ...(await r.json()) };
  } catch {
    return defaultConfig;
  }
}

export function workflowUrl(cfg: RuntimeConfig, workflowId: string): string | null {
  if (!cfg.temporalUiUrl) return null;
  return `${cfg.temporalUiUrl.replace(/\/$/, "")}/namespaces/${encodeURIComponent(cfg.temporalNamespace)}/workflows/${encodeURIComponent(workflowId)}`;
}
