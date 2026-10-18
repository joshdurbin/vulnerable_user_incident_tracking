import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useState } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { api, authHeaders, unwrap, type SourceTestResult, type SourceType } from "../../api/client";
import { ErrorNote, Loading } from "../../components/bits";
import { emptyForm, formFromDetail, inputFromForm, quickProblems, type SourceFormState } from "./adminForm";
import { useAdmin } from "./AdminLayout";

export default function SourceFormPage() {
  const { slug } = useParams();
  const creating = slug === undefined;
  const { types } = useAdmin();
  const qc = useQueryClient();
  const nav = useNavigate();
  const [form, setForm] = useState<SourceFormState>(emptyForm());
  const [loaded, setLoaded] = useState(creating);

  const existing = useQuery({
    queryKey: ["admin", "source", slug],
    enabled: !creating,
    queryFn: async () => unwrap(await api.GET("/admin/sources/{slug}", { params: { path: { slug: slug ?? "" } }, headers: authHeaders() })),
  });
  useEffect(() => {
    if (existing.data && !loaded) { setForm(formFromDetail(existing.data)); setLoaded(true); }
  }, [existing.data, loaded]);

  const type: SourceType | undefined = types.find((t) => t.type === form.type);
  const problems = quickProblems(form, type, creating);
  const set = <K extends keyof SourceFormState>(k: K, v: SourceFormState[K]) => setForm((f) => ({ ...f, [k]: v }));

  const save = useMutation({
    mutationFn: async () => {
      const body = inputFromForm(form, type);
      return creating
        ? unwrap(await api.POST("/admin/sources", { body, headers: authHeaders() }))
        : unwrap(await api.PUT("/admin/sources/{slug}", { params: { path: { slug: slug ?? "" } }, body, headers: authHeaders() }));
    },
    onSuccess: (saved) => {
      qc.invalidateQueries({ queryKey: ["admin"] });
      // Saved, but the schedule could not be updated: stay and say so, rather than hiding it behind a redirect.
      if (!saved.schedule_error) nav(`/admin/sources/${saved.slug}`);
    },
  });
  const test = useMutation({
    mutationFn: async () => unwrap(await api.POST("/admin/source-tests", { body: inputFromForm(form, type), headers: authHeaders() })),
  });

  if (!creating && existing.isPending) return <Loading what="source" />;
  if (!creating && existing.isError) return <ErrorNote error={existing.error} />;

  return (
    <>
      <p><Link to={creating ? "/admin/sources" : `/admin/sources/${slug}`}>← Back</Link></p>
      <form
        className="card source-form" aria-label={creating ? "Add source" : "Edit source"}
        onSubmit={(e) => { e.preventDefault(); if (problems.length === 0) save.mutate(); }}
      >
        <h2>{creating ? "Add a source" : `Edit ${existing.data?.name ?? slug}`}</h2>

        <div className="form-grid">
          <label>
            Type
            <select value={form.type} onChange={(e) => set("type", e.target.value)}>
              <option value="">Choose…</option>
              {types.map((t) => <option key={t.type} value={t.type}>{t.label}</option>)}
            </select>
            {type && <span className="hint">{type.description}</span>}
          </label>

          <label>
            Name
            <input value={form.name} onChange={(e) => set("name", e.target.value)} placeholder="Oaklandside" />
          </label>

          <label>
            Slug
            <input
              value={form.slug} onChange={(e) => set("slug", e.target.value.toLowerCase())} disabled={!creating}
              placeholder="oaklandside" pattern="[a-z0-9][a-z0-9-]{1,62}"
            />
            <span className="hint">{creating ? "Lowercase letters, digits and dashes. It names the source's schedule, so it cannot be changed later." : "The slug identifies the source and cannot be changed."}</span>
          </label>

          {type?.url === "required" && (
            <label className="wide">
              URL
              <input type="url" value={form.url} onChange={(e) => set("url", e.target.value)} placeholder={type.url_hint ?? "https://"} />
            </label>
          )}

          <label>
            Run every (minutes)
            <input type="number" min={1} step={1} value={form.pollMinutes} onChange={(e) => set("pollMinutes", e.target.value)} />
          </label>

          <label className="check">
            <input type="checkbox" checked={form.enabled} onChange={(e) => set("enabled", e.target.checked)} />
            Enabled
            <span className="hint">A disabled source keeps its history but does not run.</span>
          </label>
        </div>

        {type && (
          <fieldset>
            <legend>Where it is relevant{type.uses_coverage ? "" : " (informational)"}</legend>
            <div className="form-grid">
              <label>Country <input value={form.country} onChange={(e) => set("country", e.target.value)} placeholder="US" /></label>
              <label>States <input value={form.states} onChange={(e) => set("states", e.target.value)} placeholder="CA, OR" /></label>
              <label>Cities <input value={form.cities} onChange={(e) => set("cities", e.target.value)} placeholder="Oakland, Berkeley" /></label>
            </div>
          </fieldset>
        )}

        {type && (type.crawl.allowed_domains || type.crawl.max_pages || type.crawl.max_depth || type.crawl.url_patterns || type.crawl.render) && (
          <fieldset>
            <legend>Crawl limits</legend>
            <div className="form-grid">
              {type.crawl.allowed_domains && (
                <label className="wide">
                  Allowed domains{type.crawl.require_domains ? " (required)" : ""}
                  <textarea rows={3} value={form.allowedDomains} onChange={(e) => set("allowedDomains", e.target.value)} placeholder={"police.example.gov"} />
                  <span className="hint">One per line. The crawl never leaves these.</span>
                </label>
              )}
              {type.crawl.url_patterns && (
                <label className="wide">
                  Link patterns
                  <textarea rows={2} value={form.urlPatterns} onChange={(e) => set("urlPatterns", e.target.value)} placeholder={"/news/\\d{4}/"} />
                  <span className="hint">Regular expressions, one per line. Only matching links are followed. Empty follows all within the allowed domains.</span>
                </label>
              )}
              {type.crawl.max_pages && <label>Max pages <input type="number" min={1} value={form.maxPages} onChange={(e) => set("maxPages", e.target.value)} placeholder="default" /></label>}
              {type.crawl.max_depth && <label>Max depth <input type="number" min={1} value={form.maxDepth} onChange={(e) => set("maxDepth", e.target.value)} placeholder="default" /></label>}
              {type.crawl.render && (
                <label>
                  JavaScript rendering
                  <select value={form.render} onChange={(e) => set("render", e.target.value)}>
                    <option value="">Default (off)</option>
                    <option value="http_only">Never</option>
                    <option value="auto">Only when a page has no text</option>
                    <option value="always">Always</option>
                  </select>
                </label>
              )}
            </div>
          </fieldset>
        )}

        {problems.length > 0 && (
          <ul className="hint-list" aria-label="Still needed">{problems.map((p) => <li key={p}>{p}</li>)}</ul>
        )}
        {save.isError && <ErrorNote error={save.error} />}
        {save.isSuccess && save.data.schedule_error && (
          <p className="notice" role="status">Saved. {save.data.schedule_error} <Link to={`/admin/sources/${save.data.slug}`}>Open source</Link></p>
        )}

        <div className="actions">
          <button className="primary" type="submit" disabled={problems.length > 0 || save.isPending}>{creating ? "Add source" : "Save changes"}</button>
          <button type="button" disabled={!type || test.isPending || (type.url === "required" && !form.url.trim())} onClick={() => test.mutate()}
            title="Run the source once without saving anything">
            {test.isPending ? "Testing…" : "Test"}
          </button>
          <span className="muted small">Test runs this configuration once and shows what it finds. Nothing is saved.</span>
        </div>
        {test.isError && <ErrorNote error={test.error} />}
        {test.data && <TestResultView r={test.data} />}
      </form>
    </>
  );
}

export function TestResultView({ r }: { r: SourceTestResult }) {
  return (
    <section className="test-result" aria-label="Test result">
      {r.ok
        ? <p className="ok" role="status">Worked: {r.found} found, {r.unique_urls} unique, in {(r.duration_ms / 1000).toFixed(1)} s ({r.requests} request{r.requests === 1 ? "" : "s"}).</p>
        : <p className="error" role="alert">Failed: {r.error}</p>}
      {r.truncated && <p className="notice">The source hit a limit, so there may be more than shown.</p>}
      {r.unresolved > 0 && <p className="notice">{r.unresolved} link{r.unresolved === 1 ? " is" : "s are"} not the publisher's own address yet.</p>}
      {r.warnings.length > 0 && <ul className="hint-list">{r.warnings.map((w) => <li key={w}>{w}</li>)}</ul>}
      {r.samples.length > 0 && (
        <>
          <h3>What it found{r.unique_urls > r.samples.length ? ` (first ${r.samples.length})` : ""}</h3>
          <ul className="plain">
            {r.samples.map((x) => (
              <li key={x.url}>
                <a href={x.url} target="_blank" rel="noreferrer noopener">{x.title || x.url}</a>
                <div className="muted small">{x.url}{x.published_at ? ` · ${new Date(x.published_at).toLocaleString()}` : ""}</div>
              </li>
            ))}
          </ul>
        </>
      )}
      {r.ok && r.samples.length === 0 && <p className="muted">It ran but found no articles in the recent window.</p>}
    </section>
  );
}
