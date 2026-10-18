# A source is failing or stale

Alerts: `CollisionSourceFailing` (several runs failed in a row), `CollisionSourceStale` (no success for three poll
intervals).

1. **Which source and why.** Admin, Sources: the Health column says failing or stale and why; open the source for its
   run history (each failed run shows its error). `collisionctl sources list` shows the same state from the command line.
2. **Is its schedule running?** A source shows as Paused when its schedule is paused. That is deliberate and survives
   deploys; "Resume" on the Sources page undoes it. A source with no schedule at all (saved while Temporal was down)
   shows "no schedule": use "Sync schedules".
3. **Look at the last runs.** The source page's run history shows the error per run. For more, search the Temporal UI by
   `CollisionSource = "<slug>"` and `CollisionKind = "discovery"` and read the failed activity.
4. **Common causes**
   - *Rate limited (GDELT, Google News).* GDELT allows one request per five seconds. Persistent 429s are retried later by
     Temporal; if they continue, lower the poll rate by editing the source's interval.
   - *Feed URL moved or returned HTML.* Open it in a browser. Fix the URL on the source (Admin, Sources, Edit) and use
     "Test" to confirm it returns articles before saving.
   - *Site now blocks the crawler.* Check robots.txt and the crawler logs. Do not work around a block.
   - *Google News links not resolving.* They are opaque unless `discovery.resolveGoogleNews` is on, which relies on an
     undocumented endpoint and can break without notice.
   - *"Test" says no discovery worker answered.* The Go worker is not polling the discovery queue; see
     `pipeline-stalled.md`.
5. **Disable what you cannot fix:** untick Enabled on the source (Edit). That pauses its schedule and keeps its history.
6. **After fixing,** "Run now" on the source, and confirm the next run succeeds and the failure count clears.
