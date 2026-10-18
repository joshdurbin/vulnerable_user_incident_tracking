# Runbooks

| Situation | Runbook |
|---|---|
| First deploy, upgrades, rollback | [deploy.md](deploy.md) |
| Alert: a source is failing or stale | [source-failing.md](source-failing.md) |
| Alert: no new incidents, worker or crawler not polling, queue backlog | [pipeline-stalled.md](pipeline-stalled.md) |
| Changing the model or prompt, or too many articles being classified | [model-and-prompt-changes.md](model-and-prompt-changes.md) |
| Merging, splitting and reviewing incidents | [incidents.md](incidents.md) |

Conventions used below: `collisionctl` runs inside the cluster, for example
`kubectl -n collision-tracker exec deploy/collision-tracker-api -- /app <command>`, or from a laptop with
`COLLISION_DATABASE_URL` and `COLLISION_TEMPORAL_ADDRESS` pointed at port-forwards. Commands that touch Temporal need
`--temporal-namespace collisions`. The release is assumed to be named `collision-tracker`.
