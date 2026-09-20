# How should an analytical definition and authorized query be designed?

## Problems solved

- Gives analytical measures, dimensions, filters, result types, paging, and source authorization one reusable contract.

## Business scenarios

- Querying monthly revenue and order count grouped by region with bounded date parameters.
- Calculating inventory aging, service-level performance, or other grouped metrics that ordinary Object lists cannot express.

## Use when

Use a Report definition for reusable measures, dimensions, grouped results, parameters, and stable paged analytical output.

## Do not use when

Do not create a Report for a transactional list/detail query with no aggregate or reusable analytical meaning.

## How to use

Name the business question, authorized source Objects, measures, dimensions, filters, result schema, ordering, and paging limits. Compile against the source definitions rather than accepting raw SQL from users.

## Adaptation cookbook

| Business requirement | Adapt with | Concrete implementation | Wrong adaptation |
| --- | --- | --- | --- |
| Finance queries monthly revenue and order count by region | Report definition with typed measures, time/region dimensions, bounded parameters, and authorized query | Define measure formulas and result types once; validate date range and grouping; push the caller's row scope to source reads; return stable paging/order | Loading all orders into a Handler and aggregating in memory or trusting a region filter supplied by the client |
| Operations analyzes inventory aging buckets | Report measure/dimension model over authorized inventory facts | Define as-of time, bucket boundaries, null handling, and warehouse dimension; expose a typed result contract | Adding denormalized dashboard-only fields to every inventory record without a governed definition |
| User opens an ordinary searchable customer list | Object list query, not Report | Use typed filters, sorting, paging, and normal Object authorization | Creating a Report for straightforward record retrieval with no analytical measure or grouping |
| A measure spans several owners | Explicit Report source contracts and composition | Each source exposes authorized facts/aggregates; Report joins only on stable approved keys and documents freshness | Giving Report unrestricted database access across owner tables |

## Example

“Monthly revenue by region” defines month and region dimensions, revenue and order-count measures, bounded date parameters, and a typed result. “My five latest orders” remains an Object query.

## Permissions and scope

A Role needs exact `report.query` authority and source-compatible data scope. Report permission never grants Object CRUD, and Object read alone does not automatically grant every Report.

## Boundaries

Report owns analytical semantics; source owners own underlying business records and authorization facts.
