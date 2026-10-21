import { useEffect, useState } from "react";
import { SEVERITIES, VRU_OPTIONS, useFilters, type VruCategory } from "../filters";
import { label } from "../format";

/** Free-text search, submitted with Enter so a request is not sent per keystroke. */
function SearchBox() {
  const [f, set] = useFilters();
  const [text, setText] = useState(f.q ?? "");
  useEffect(() => setText(f.q ?? ""), [f.q]); // follows the URL (back button, a shared link)
  return (
    <form
      className="search" role="search" aria-label="Search incidents"
      onSubmit={(e) => { e.preventDefault(); set({ q: text.trim() }); }}
    >
      <label>
        Search
        <input type="search" value={text} maxLength={200} placeholder="street, city, “fatal cyclist”, a headline" onChange={(e) => setText(e.target.value)} />
      </label>
      <button type="submit">Search</button>
      {f.q && <button type="button" onClick={() => set({ q: "" })}>Clear</button>}
    </form>
  );
}

export function FilterBar({ search = false }: { search?: boolean }) {
  const [f, set] = useFilters();
  const toggleVru = (v: VruCategory) => set({ vru: f.vru.includes(v) ? f.vru.filter((x) => x !== v) : [...f.vru, v] });
  return (
    <>
    {search && <SearchBox />}
    <div className="filters" role="group" aria-label="Filters">
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
    </>
  );
}
