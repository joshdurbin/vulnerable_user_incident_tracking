import { SEVERITIES, VRU_OPTIONS, useFilters } from "../filters";
import { label } from "../format";

export function FilterBar() {
  const [f, set] = useFilters();
  const toggleVru = (v: string) => set({ vru: f.vru.includes(v) ? f.vru.filter((x) => x !== v) : [...f.vru, v] });
  return (
    <div className="filters" role="search" aria-label="Filters">
      <label>
        From
        <input type="date" value={f.from ?? ""} onChange={(e) => set({ from: e.target.value })} />
      </label>
      <label>
        To
        <input type="date" value={f.to ?? ""} onChange={(e) => set({ to: e.target.value })} />
      </label>
      <label>
        State
        <input type="text" maxLength={2} size={3} placeholder="CA" value={f.state ?? ""} onChange={(e) => set({ state: e.target.value.toUpperCase() })} />
      </label>
      <label>
        Severity
        <select value={f.severity ?? ""} onChange={(e) => set({ severity: e.target.value })}>
          <option value="">Any</option>
          {SEVERITIES.map((s) => (
            <option key={s} value={s}>{label(s)}</option>
          ))}
        </select>
      </label>
      <label>
        Hit and run
        <select value={f.hit_and_run ?? ""} onChange={(e) => set({ hit_and_run: e.target.value })}>
          <option value="">Any</option>
          <option value="yes">Yes</option>
          <option value="no">No</option>
        </select>
      </label>
      <fieldset className="chips">
        <legend>Road user</legend>
        {VRU_OPTIONS.map((v) => (
          <button key={v} type="button" className={f.vru.includes(v) ? "chip on" : "chip"} aria-pressed={f.vru.includes(v)} onClick={() => toggleVru(v)}>
            {label(v)}
          </button>
        ))}
      </fieldset>
    </div>
  );
}
