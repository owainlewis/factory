# Run health metrics

Factory's overview reports agent Jobs, not generic process or GitHub activity.
Every metric uses one cohort: Jobs admitted during the selected time window after
the Definition, repository, and Runner filters are applied. `All retained` has
no lower time boundary.

| Metric | Formula |
| --- | --- |
| Active Jobs | Count of Jobs whose effective state is `queued`, `preparing`, or `running` |
| Blocked Jobs | Count of Jobs whose effective state is `blocked` |
| Succeeded Jobs | Count of Jobs whose effective state is `succeeded` |
| Failed Jobs | Count of Jobs whose effective state is `failed` |
| Success rate | Succeeded Jobs divided by succeeded plus failed Jobs; cancellations are excluded |
| Average queue time | Mean of first agent start minus Job admission for Jobs that started |
| Average cycle time | Mean of terminal time minus Job admission for terminal Jobs |
| Throughput | Count of succeeded, failed, and cancelled Jobs in the cohort |

The effective state comes from the Job's Execution after one exists, otherwise
from the durable Job admission record. First start is the earliest stored
Attempt start. Terminal time prefers the terminal Execution's update time, then
the latest stored Attempt completion when an Execution transition time is not
available, then the Job update time. The final fallback covers a blocked Job
cancelled before an Execution exists. A retry does not create another Job and
therefore does not inflate throughput.

The overview returns at most the 100 most recently admitted matching Jobs for
drill-down. Aggregate formulas include the full matching cohort. Filter choices
come from retained Runs and Jobs, so archived Definitions and historical target
identities remain understandable.
