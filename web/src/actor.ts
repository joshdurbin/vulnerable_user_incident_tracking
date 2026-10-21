/** The name recorded in the audit trail with a decision or correction. Kept per browser, not per tab. */
const ACTOR_KEY = "collision.actor";
export const actorName = {
  get: () => { try { return localStorage.getItem(ACTOR_KEY) ?? ""; } catch { return ""; } },
  set: (v: string) => { try { localStorage.setItem(ACTOR_KEY, v); } catch { /* storage unavailable */ } },
};
