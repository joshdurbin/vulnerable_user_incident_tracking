# Deploy

## What a release does

Argo CD syncs the Helm chart in `deploy/helm/collision-tracker`. Hooks run in order:

1. **PreSync, `temporal-setup`:** creates the `collisions` Temporal namespace (or updates its retention) and the seven
   custom search attributes. Idempotent.
2. **PreSync, `migrate`:** applies database migrations. The database role is not a superuser, so the extensions
   (`postgis`, `pg_trgm`, `btree_gist`, `pgcrypto`) must already exist. The infra repo's `Database` resource creates them.
3. The api, worker, crawler and web Deployments roll out.
4. **PostSync, `bootstrap`:** adds any sources and queries that do not exist yet (never overwriting what an operator
   changed), then creates a Temporal Schedule for every source (keeping any pause an operator set).

If a PreSync Job fails, nothing rolls out and the running version keeps serving. Read the Job's logs first:
`kubectl -n collision-tracker logs job/collision-tracker-migrate`.

## First deploy checklist

- [ ] Infra repo: PostGIS image on the shared Postgres cluster, `collisions` role and database with extensions, secrets,
      AppProject entries, Argo Application. All of it is in the infra change set for this app.
- [ ] Secrets exist in `collision-tracker`: `collision-db` (key `url`), `collision-api` (key `admin-token`).
- [ ] In Bifrost: an OpenRouter provider and key, and the model you will use. Set `llm.model` in the Application values.
- [ ] Images exist in ghcr.io (built by CI on `main`) and the cluster can pull them. If the packages are private, Zot and
      the nodes need credentials; the simplest fix is to make the packages public.
- [ ] Sync the Application. Nothing is collected yet: no sources are built in. Open `/admin`, enter the admin token and add
      sources (or `collisionctl sources import --file docs/examples/sources.yaml` for a starter set, then "Sync schedules").
- [ ] Check the Admin page: sources are listed, each shows a next run time, and "Test" on a source returns articles.
- [ ] Start small. Leave most sources disabled, run one with "Run now", and watch an article go through before enabling
      the rest.

## Upgrade from a version without duplicate detection (migration 00009)

The migration only adds columns, so old pods keep working. Articles stored before it have no fingerprint and are never
matched against. To include those that still hold their text, run `collisionctl articles fingerprint` once after the deploy
(safe to repeat). It does not change incidents or mark old articles as duplicates.

## Upgrade

Change the image tags (prefer digests) in the Application values in the infra repo and let Argo sync. Migrations are
forward-only. Write them so the previous version still works against the new schema; the old pods keep running until the
new ones are ready.

## Roll back

Revert the image change in git and sync. If a migration has run, the old version must still work with the new schema
(see above). Do not run `migrate down` against production data.

## Workflow code changes and in-flight workflows

Workflow code must stay deterministic for workflows already running. Before changing a workflow's steps, run the replay
tests (`make test-integration` replays recorded histories) and use Temporal's `workflow.GetVersion` for the change. The
article workflow is short-lived, so a draining deploy is also an option: pause the schedules
(`collisionctl schedules pause <slug>`), wait for open workflows to finish, deploy, resume.

## Backups

All state is in PostgreSQL (CloudNativePG backups cover it). Temporal history is disposable: it is kept for the namespace
retention period (72 hours by default) and everything durable is in the application database. Losing Temporal loses
schedules and open workflows only; `collisionctl schedules sync` recreates the schedules, and unfinished articles are
re-listed as pending by the next discovery run.
