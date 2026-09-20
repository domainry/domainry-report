# When should a Report use a materialized snapshot?

## Problems solved

- Materializes expensive or time-stable analytical results so dashboards do not recompute large aggregates on every read.

## Business scenarios

- Publishing a weekday morning finance dashboard from the latest governed revenue snapshot.
- Freezing month-end or compliance figures with an explicit as-of time and controlled refresh.

## Use when

Use a snapshot when the product needs a stable “as of” result, expensive aggregation must be bounded, or refresh timing is explicitly required.

## Do not use when

Do not materialize by default when live query satisfies freshness, latency, and cost. A snapshot is not a writable business record.

## How to use

Define snapshot identity, source definition version, freshness, refresh operation, replacement semantics, and failure evidence. Scheduler may trigger refresh but Report performs it.

## Adaptation cookbook

| Business requirement | Adapt with | Concrete implementation | Wrong adaptation |
| --- | --- | --- | --- |
| Finance dashboard should open quickly every weekday morning | Scheduler-triggered Report snapshot | Scheduler starts the published Report/version at 09:00 business timezone; snapshot stores parameters, as-of time, scope semantics, result, and refresh outcome | Caching an unrestricted query result without definition/version or authorization context |
| Month-end figures must remain reproducible | Immutable named snapshot with explicit cutoff | Freeze source cutoff/as-of time and definition version; publish only after successful complete calculation; retain evidence under policy | Recomputing “month end” from current mutable data on every view |
| Source data changes every second and query is already cheap | Live Report query | Execute against current authorized facts and avoid snapshot staleness/operations overhead | Adding scheduled materialization merely because a dashboard exists |
| Refresh fails after prior snapshot exists | Preserve last successful snapshot plus visible freshness/failure state | Do not replace good data with partial output; expose last success time and failed refresh; retry through Scheduler policy | Silently showing stale data as current or deleting the last good snapshot before refresh completes |

## Example

A weekday 09:00 finance dashboard reads the latest governed revenue snapshot. Scheduler claims the time window; Report executes and publishes the snapshot atomically.

## Permissions and scope

Snapshot reads and refresh are separate. The refresh service Role receives only the Report operation and truthful source scope.

## Boundaries

Scheduler owns when; Report owns what is calculated and how result versions are exposed.
