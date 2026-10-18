import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { api, authHeaders, unwrap, unwrapEmpty, type Query } from "../../api/client";
import { ErrorNote, Loading } from "../../components/bits";
import { label } from "../../format";
import { useAdmin } from "./AdminLayout";
import { typeLabel } from "./adminForm";

interface QueryForm {
  name: string;
  family: string;
  text: string;
  providers: string[];
  region: string;
  enabled: boolean;
}

const blank: QueryForm = { name: "", family: "", text: "", providers: [], region: "", enabled: true };
const toForm = (q: Query): QueryForm => ({ name: q.name, family: q.family, text: q.text, providers: q.providers ?? [], region: q.region ?? "", enabled: q.enabled });

export default function QueriesPage() {
  const { types } = useAdmin();
  const qc = useQueryClient();
  const [form, setForm] = useState<QueryForm>(blank);
  const [editing, setEditing] = useState<string | null>(null);
  const searchTypes = types.filter((t) => t.uses_queries);

  const list = useQuery({
    queryKey: ["admin", "queries"],
    queryFn: async () => unwrap(await api.GET("/admin/queries", { headers: authHeaders() })),
  });
  const reset = () => { setForm(blank); setEditing(null); };
  const done = () => { qc.invalidateQueries({ queryKey: ["admin", "queries"] }); reset(); };

  const save = useMutation({
    mutationFn: async () => {
      const body: Query = { name: form.name.trim(), family: form.family.trim(), text: form.text.trim(), providers: form.providers, enabled: form.enabled, ...(form.region.trim() ? { region: form.region.trim() } : {}) };
      return editing
        ? unwrap(await api.PUT("/admin/queries/{name}", { params: { path: { name: editing } }, body, headers: authHeaders() }))
        : unwrap(await api.POST("/admin/queries", { body, headers: authHeaders() }));
    },
    onSuccess: done,
  });
  const del = useMutation({
    mutationFn: async (name: string) => unwrapEmpty(await api.DELETE("/admin/queries/{name}", { params: { path: { name } }, headers: authHeaders() })),
    onSuccess: done,
  });
  const toggle = useMutation({
    mutationFn: async (q: Query) => unwrap(await api.PUT("/admin/queries/{name}", { params: { path: { name: q.name } }, body: { ...q, enabled: !q.enabled }, headers: authHeaders() })),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["admin", "queries"] }),
  });
  const set = <K extends keyof QueryForm>(k: K, v: QueryForm[K]) => setForm((f) => ({ ...f, [k]: v }));
  const ready = form.name.trim() && form.family.trim() && form.text.trim();

  return (
    <>
      <p className="muted">
        Search-style sources (GDELT, Google News) run these queries instead of reading one address. Wrap every OR group in parentheses and quote phrases:{" "}
        <code>(pedestrian OR walker) (struck OR hit)</code>. A query with no source types selected runs on all of them.
      </p>
      {list.isPending && <Loading what="queries" />}
      {list.isError && <ErrorNote error={list.error} />}
      {(del.isError || toggle.isError) && <ErrorNote error={del.error ?? toggle.error} />}
      {list.data?.length === 0 && <p className="card">No queries yet, so search sources have nothing to search for. Add one below.</p>}
      {list.data && list.data.length > 0 && (
        <div className="scroll">
          <table className="data">
            <thead><tr><th>Name</th><th>Family</th><th>Query</th><th>Runs on</th><th>Enabled</th><th></th></tr></thead>
            <tbody>
              {list.data.map((q) => (
                <tr key={q.name}>
                  <th scope="row">{q.name}</th>
                  <td>{label(q.family)}</td>
                  <td className="wrap"><code>{q.text}</code></td>
                  <td>{q.providers?.length ? q.providers.map((p) => typeLabel(types, p)).join(", ") : "All search sources"}</td>
                  <td>
                    <label className="check inline">
                      <input type="checkbox" checked={q.enabled} disabled={toggle.isPending} onChange={() => toggle.mutate(q)} aria-label={`${q.name} enabled`} />
                    </label>
                  </td>
                  <td className="actions">
                    <button type="button" onClick={() => { setForm(toForm(q)); setEditing(q.name); }}>Edit</button>
                    <button
                      type="button" className="danger" disabled={del.isPending}
                      onClick={() => { if (window.confirm(`Delete the query "${q.name}"?`)) del.mutate(q.name); }}
                    >Delete</button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      <form className="card" aria-label={editing ? "Edit query" : "Add query"} onSubmit={(e) => { e.preventDefault(); if (ready) save.mutate(); }}>
        <h2>{editing ? `Edit ${editing}` : "Add a query"}</h2>
        <div className="form-grid">
          <label>Name <input value={form.name} disabled={editing !== null} onChange={(e) => set("name", e.target.value.toLowerCase())} placeholder="pedestrian-struck" /></label>
          <label>Family <input value={form.family} onChange={(e) => set("family", e.target.value)} placeholder="pedestrian" /></label>
          <label>Region <input value={form.region} onChange={(e) => set("region", e.target.value)} placeholder="optional" /></label>
          <label className="wide">
            Query
            <textarea rows={2} value={form.text} onChange={(e) => set("text", e.target.value)} placeholder={'(pedestrian OR walker) (struck OR hit)'} />
          </label>
          <fieldset className="wide">
            <legend>Runs on</legend>
            {searchTypes.map((t) => (
              <label key={t.type} className="check inline">
                <input
                  type="checkbox" checked={form.providers.includes(t.type)}
                  onChange={(e) => set("providers", e.target.checked ? [...form.providers, t.type] : form.providers.filter((p) => p !== t.type))}
                />
                {t.label}
              </label>
            ))}
            <span className="hint">None selected means all search sources.</span>
          </fieldset>
          <label className="check"><input type="checkbox" checked={form.enabled} onChange={(e) => set("enabled", e.target.checked)} /> Enabled</label>
        </div>
        {save.isError && <ErrorNote error={save.error} />}
        <div className="actions">
          <button className="primary" type="submit" disabled={!ready || save.isPending}>{editing ? "Save changes" : "Add query"}</button>
          {editing && <button type="button" onClick={reset}>Cancel</button>}
        </div>
      </form>
    </>
  );
}
