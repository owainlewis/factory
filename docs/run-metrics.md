# Run health metrics

Factory's overview reports Routine runs, stored as Work. Performance metrics use
one fixed cohort: Work admitted during the previous 24 hours. Operational status
metrics such as active Work and Worker availability cover all retained records.

| Metric | Formula |
| --- | --- |
| Runs | Count of Work admitted in the cohort |
| Completed runs | Count of cohort Work with a terminal time |
| Completion rate | Completed runs divided by all runs in the cohort |
| Average queue time | Mean of first Target start minus Work admission for runs that started |
| Average cycle time | Mean of terminal time minus Work admission for completed runs |

The first start is the earliest stored Attempt start across the Work's Targets.
The terminal time is set when every Target has reached a terminal state. A
Target retry stays inside the same Work record, so it does not inflate the run
count or rewrite the first start. Empty cohorts show `No data` for rates and
durations instead of reporting a misleading zero.
