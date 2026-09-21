# How should an analytical definition and authorized query be designed?

## Problems solved

- Gives analytical measures, dimensions, filters, result types, paging, and source authorization one reusable contract.

## Business scenarios

- Querying monthly revenue and order count grouped by region with bounded date parameters.
- Calculating inventory aging, service-level performance, or other grouped metrics that ordinary Object lists cannot express.
- Paging a detailed analytical result under a deterministic order and cursor so one query boundary does not repeat or skip rows.
- Applying both row scope and field permission before a sensitive profit measure reaches the result.

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
| A result has thousands of detail rows | Stable Object SQL order and cursor | Include a deterministic `ORDER BY` and bounded `LIMIT`; bind paging to the same definition/version, parameters, scope, and snapshot/as-of boundary | Offset paging over changing data with no tie-breaker or letting the client invent sort SQL |

## Example

`monthly_sales_by_region` may use one `object_sql_v1` SELECT such as `SELECT o.\`region\` AS region, DATE_BUCKET('month', o.\`ordered_at\`) AS month, SUM(o.\`amount\`) AS revenue, COUNT(o.\`id\`) AS order_count FROM \`sales_order\` o WHERE o.\`ordered_at\` >= :from_time AND o.\`ordered_at\` < :to_time GROUP BY o.\`region\`, DATE_BUCKET('month', o.\`ordered_at\`) ORDER BY month, region LIMIT 1000`. Parameters are declared as `from_time: datetime!` and `to_time: datetime!`; result aliases are typed as dimensions or measures. Runtime derives source Object/fields, pushes the caller's row scope, and checks field authorization before execution.

The closed grammar permits one SELECT, bounded INNER/LEFT JOIN, WHERE/GROUP BY/HAVING/ORDER BY/LIMIT, the published aggregate/function set, qualified backtick-quoted model identifiers, and named typed parameters. It rejects arbitrary table names, `SELECT *`, string literals, subqueries, CTEs, UNION, window functions, and DML/DDL. A regional manager therefore cannot obtain another region by changing a parameter, and a sensitive profit column remains denied without its field grant. “My five latest orders” remains an Object query.

## Permissions and scope

A Role needs exact `report.query` authority and source-compatible data scope. Report permission never grants Object CRUD, and Object read alone does not automatically grant every Report.

## Boundaries

Report owns analytical semantics; source owners own underlying business records and authorization facts.
