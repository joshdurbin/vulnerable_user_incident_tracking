import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { NavLink, Outlet, useOutletContext } from "react-router-dom";
import { ApiError, adminToken, api, authHeaders, unwrap, type SourceType } from "../../api/client";
import { ErrorNote, Loading } from "../../components/bits";

export interface AdminContext {
  types: SourceType[];
}
export const useAdmin = () => useOutletContext<AdminContext>();

/**
 * Everything under /admin is behind the admin token, reads included: source configuration and
 * health are not public. The token is checked by asking for the source types, which every page needs.
 */
export default function AdminLayout() {
  const qc = useQueryClient();
  const [token, setToken] = useState(adminToken.get());
  const [draft, setDraft] = useState("");
  const types = useQuery({
    queryKey: ["admin", "source-types", token],
    enabled: token !== "",
    retry: false,
    queryFn: async () => unwrap(await api.GET("/admin/source-types", { headers: authHeaders() })),
  });

  const save = (t: string) => {
    adminToken.set(t);
    setToken(t);
    qc.removeQueries({ queryKey: ["admin"] });
  };
  const rejected = types.error instanceof ApiError && types.error.status === 401;

  if (token === "" || rejected) {
    return (
      <>
        <h1>Admin</h1>
        <form className="card gate" onSubmit={(e) => { e.preventDefault(); save(draft.trim()); setDraft(""); }}>
          <h2>Admin token</h2>
          <p className="muted">
            Source configuration and health are only shown to administrators. The token is kept in this tab only and forgotten when it closes.
          </p>
          {rejected && <p className="error" role="alert">That token was not accepted.</p>}
          <label>
            Token <input type="password" value={draft} onChange={(e) => setDraft(e.target.value)} autoComplete="off" autoFocus />
          </label>
          <div className="actions"><button className="primary" type="submit" disabled={!draft.trim()}>Continue</button></div>
        </form>
      </>
    );
  }

  return (
    <>
      <div className="admin-head">
        <h1>Admin</h1>
        <nav aria-label="Admin" className="subnav">
          <NavLink to="/admin" end>Overview</NavLink>
          <NavLink to="/admin/sources">Sources</NavLink>
          <NavLink to="/admin/queries">Queries</NavLink>
          <NavLink to="/admin/jobs">Backfill &amp; reprocess</NavLink>
        </nav>
        <button type="button" className="linklike small" onClick={() => save("")}>Forget token</button>
      </div>
      {types.isPending && <Loading what="admin" />}
      {types.isError && <ErrorNote error={types.error} />}
      {types.data && <Outlet context={{ types: types.data } satisfies AdminContext} />}
    </>
  );
}
