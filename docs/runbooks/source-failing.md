# A source is failing or stale

Alerts: `CollisionSourceFailing` (several runs failed in a row), `CollisionSourceStale` (no success for three poll
intervals).

1. **Which source and why.** Operations page, "Discovery sources", or
   `collisionctl sources list` (shows the failure count and the last error).
2. **Is its schedule running?** `collisionctl schedules list`. A schedule someone paused in the Temporal UI stays paused
   across deploys by design. Resume with `collisionctl schedules resume <slug>`.
3. **Look at the last runs.** In the Temporal UI, search workflows by `CollisionSource = "<slug>"` and
   `CollisionKind = "discovery"` and read the failed one's activity error.
4. **Common causes**
   - *Rate limited (GDELT, Google News).* GDELT allows one request per five seconds. Persistent 429s are retried later by
     Temporal; if they continue, lower the poll rate by editing the source's interval.
   - *Feed URL moved or returned HTML.* Open it in a browser. Fix the URL on the source in the admin UI
     (Admin, Sources) and use "Test" to confirm it returns articles before saving.
   - *Site now blocks the crawler.* Check robots.txt and the crawler logs. Do not work around a block.
   - *Google News links not resolving.* They are opaque unless `discovery.resolveGoogleNews` is on, which relies on an
     undocumented endpoint and can break without notice.
5. **Disable what you cannot fix:** `collisionctl schedules pause <slug>`, and set the source disabled in the database or
   seed file so a later sync does not re-enable it.
6. **After fixing,** "Run now" on the Operations page (or `collisionctl schedules trigger <slug>`), and confirm the failure
   count drops to zero.
