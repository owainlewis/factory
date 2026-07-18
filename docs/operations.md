# Factory operation

`factory run` validates `gh` and Codex subscription authentication, polls all
configured repositories, and dispatches durable ready-ticket tasks. Global and
per-repository concurrency are controlled by `max_concurrent_runs` and
`max_concurrent_runs_per_repository`. The per-repository value defaults to 1.

Factory requires the conventional `factory:ready` and
`factory:needs-review` labels to be created by repository maintainers. Factory
does not create, remove, or otherwise mutate labels itself. The delegated
workflow owns ticket and pull-request updates.

On Ctrl-C, Factory stops polling and claiming immediately, cancels active
Codex process trees, waits for the workers to record `cancelled`, and exits.
Queued tasks remain durable for the next start. Failed and cancelled runs keep
their bounded output, error, session, ticket, branch, and pull-request context
for inspection. Factory never merges software pull requests.

`factory run --once` performs one discovery poll and exits without claiming or
launching tasks. It is intended for setup checks and safe polling smoke tests.
