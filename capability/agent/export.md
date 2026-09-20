# How should a Report prepare a governed offline export?

## Problems solved

- Preserves one Report definition and authorization contract when an online analytical result must become an auditable offline file.

## Business scenarios

- Finance exports monthly revenue by region using the same measures and scope as the online Report.
- A large analytical result is prepared for durable CSV delivery through Data Exchange without widening row access.

## Use when

Use export preparation when a scoped analytical result must be stable enough for a durable downloadable artifact.

## Do not use when

Do not use it for online-only pages or let a browser crawl every page and assemble a file.

## How to use

Report executes the authorized definition and prepares canonical/stable output. Data Exchange owns job, chunks, progress, cancellation, artifact, and download; Audit records export evidence.

## Adaptation cookbook

| Business requirement | Adapt with | Concrete implementation | Wrong adaptation |
| --- | --- | --- | --- |
| Finance downloads monthly revenue by region | Report definition and scope plus Data Exchange export artifact | Resolve the same measures, parameters, and row scope as online query; create a bounded export job; Data Exchange writes/delivers the file; Audit records request and completion | Reimplementing revenue SQL in an export Handler or widening scope because output is offline |
| Analytical result is too large for an online response | Asynchronous Report export | Freeze definition version, parameters, principal/scope snapshot, and as-of semantics; page source work; expose progress and expiring artifact access | Holding one HTTP request open or dumping the source table without Report semantics |
| Compliance needs immutable evidence of who exported what | Audit events around the governed export | Record requester, Report/version, parameters, scope, artifact identity, outcome, and timestamps without placing sensitive rows in Audit | Treating provider/storage logs as sufficient export authorization evidence |
| Product needs a full object migration file | Data Exchange export, not Report | Export typed source records and relationships under migration policy | Forcing record transfer through an analytical Report definition |

## Example

Finance exports monthly revenue by region. Online and offline results use the same Report definition and scope; Data Exchange produces the CSV without widening access.

## Permissions and scope

`report.query`, export preparation, and artifact download are separate grants. The artifact must retain the requesting principal’s data scope and retention policy.

## Boundaries

Notification may announce completion and Scheduler may trigger scheduled exports, but Report remains owner of the analytical dataset.
