package controlplane

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/owainlewis/factory/internal/protocol"
)

const runHealthRecentJobsLimit = 3

func runMetricsFilter(
	start *time.Time,
	end time.Time,
	filter MetricsFilter,
) (string, []any) {
	clauses := []string{"job.admitted_at <= ?"}
	args := []any{end.UnixMilli()}
	if start != nil {
		clauses = append(clauses, "job.admitted_at >= ?")
		args = append(args, start.UnixMilli())
	}
	if filter.DefinitionID != "" {
		clauses = append(clauses, "run.definition_id = ?")
		args = append(args, filter.DefinitionID)
	}
	if filter.RepositoryID != "" {
		clauses = append(clauses, "job.repository_id = ?")
		args = append(args, filter.RepositoryID)
	}
	if filter.WorkerID != "" {
		clauses = append(clauses, "execution.assigned_worker_id = ?")
		args = append(args, filter.WorkerID)
	}
	return strings.Join(clauses, " AND "), args
}

func runMetricFactsQuery(where string, suffix string) string {
	return fmt.Sprintf(`
		WITH facts AS (
			SELECT job.id AS job_id, job.run_id, run.definition_id,
			       json_extract(run.definition_snapshot, '$.name') AS definition_name,
			       job.repository_id,
			       CASE WHEN job.repository_identity = '' THEN repository.remote_identity ELSE job.repository_identity END AS repository_identity,
			       COALESCE(execution.assigned_worker_id, '') AS worker_id,
			       COALESCE(worker.name, '') AS worker_name,
			       COALESCE(execution.state, job.state) AS effective_state,
			       job.admitted_at,
			       (SELECT MIN(attempt.started_at) FROM attempts attempt
			        WHERE attempt.execution_id = execution.id AND attempt.started_at IS NOT NULL) AS first_started_at,
			       CASE WHEN COALESCE(execution.state, job.state) IN ('succeeded', 'failed', 'cancelled')
			            THEN COALESCE(
			                execution.updated_at,
			                (SELECT MAX(attempt.completed_at) FROM attempts attempt
			                 WHERE attempt.execution_id = execution.id AND attempt.completed_at IS NOT NULL),
			                job.updated_at
			            )
			       END AS terminal_at
			FROM jobs job
			JOIN runs run ON run.id = job.run_id
			JOIN repositories repository ON repository.id = job.repository_id
			LEFT JOIN executions execution ON execution.id = job.execution_id
			LEFT JOIN workers worker ON worker.id = execution.assigned_worker_id
			WHERE %s
		)
		%s
	`, where, suffix)
}

func (s *Store) loadRunHealthMetrics(
	ctx context.Context,
	query metricsQuerier,
	start *time.Time,
	end time.Time,
	filter MetricsFilter,
	metrics *protocol.RunHealthMetrics,
) error {
	where, args := runMetricsFilter(start, end, filter)
	var averageQueueMillis, averageCycleMillis sql.NullFloat64
	err := query.QueryRowContext(ctx, runMetricFactsQuery(where, `
		SELECT COUNT(*),
		       COALESCE(SUM(effective_state IN ('queued', 'preparing', 'running')), 0),
		       COALESCE(SUM(effective_state = 'blocked'), 0),
		       COALESCE(SUM(effective_state = 'succeeded'), 0),
		       COALESCE(SUM(effective_state = 'failed'), 0),
		       COALESCE(SUM(effective_state = 'cancelled'), 0),
		       AVG(CASE WHEN first_started_at IS NOT NULL THEN MAX(0, first_started_at - admitted_at) END),
		       AVG(CASE WHEN terminal_at IS NOT NULL THEN MAX(0, terminal_at - admitted_at) END)
		FROM facts
	`), args...).Scan(
		&metrics.TotalJobs, &metrics.Active, &metrics.Blocked, &metrics.Succeeded,
		&metrics.Failed, &metrics.Cancelled, &averageQueueMillis, &averageCycleMillis,
	)
	if err != nil {
		return err
	}
	finished := metrics.Succeeded + metrics.Failed
	if finished > 0 {
		value := float64(metrics.Succeeded) / float64(finished)
		metrics.SuccessRate = &value
	}
	if averageQueueMillis.Valid {
		value := averageQueueMillis.Float64 / float64(time.Second/time.Millisecond)
		metrics.AverageQueueTimeSeconds = &value
	}
	if averageCycleMillis.Valid {
		value := averageCycleMillis.Float64 / float64(time.Second/time.Millisecond)
		metrics.AverageCycleTimeSeconds = &value
	}
	metrics.Throughput = metrics.Succeeded + metrics.Failed + metrics.Cancelled

	jobViewWhere, err := runMetricJobViewWhere(filter.JobView)
	if err != nil {
		return err
	}
	rows, err := query.QueryContext(ctx, runMetricFactsQuery(where, `
		SELECT job_id, run_id, definition_id, definition_name,
		       repository_id, repository_identity, worker_id, worker_name,
		       effective_state, admitted_at, first_started_at, terminal_at
		FROM facts
		`+jobViewWhere+`
		ORDER BY admitted_at DESC, job_id DESC
		LIMIT ?
	`), append(args, runHealthRecentJobsLimit)...)
	if err != nil {
		return err
	}
	defer rows.Close()
	metrics.Jobs = make([]protocol.RunMetricJob, 0)
	for rows.Next() {
		var job protocol.RunMetricJob
		var admittedAt int64
		var startedAt, terminalAt sql.NullInt64
		if err := rows.Scan(
			&job.JobID, &job.RunID, &job.DefinitionID, &job.DefinitionName,
			&job.RepositoryID, &job.RepositoryRemoteIdentity, &job.WorkerID, &job.WorkerName,
			&job.State, &admittedAt, &startedAt, &terminalAt,
		); err != nil {
			return err
		}
		job.AdmittedAt = fromMillis(admittedAt)
		if startedAt.Valid {
			value := fromMillis(startedAt.Int64)
			job.StartedAt = &value
		}
		if terminalAt.Valid {
			value := fromMillis(terminalAt.Int64)
			job.TerminalAt = &value
		}
		metrics.Jobs = append(metrics.Jobs, job)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return loadRunMetricOptions(ctx, query, metrics)
}

func runMetricJobViewWhere(view string) (string, error) {
	switch view {
	case "", "all":
		return "", nil
	case "active":
		return "WHERE effective_state IN ('queued', 'preparing', 'running')", nil
	case "blocked", "succeeded", "failed":
		return "WHERE effective_state = '" + view + "'", nil
	case "finished":
		return "WHERE effective_state IN ('succeeded', 'failed')", nil
	case "started":
		return "WHERE first_started_at IS NOT NULL", nil
	case "terminal":
		return "WHERE terminal_at IS NOT NULL", nil
	default:
		return "", invalid("invalid_metrics_filter", "job_view is invalid")
	}
}

func loadRunMetricOptions(
	ctx context.Context,
	query metricsQuerier,
	metrics *protocol.RunHealthMetrics,
) error {
	queries := []struct {
		target *[]protocol.MetricFilterOption
		query  string
	}{
		{&metrics.Definitions, `
			SELECT definition_id, definition_name
			FROM (
				SELECT run.definition_id,
				       json_extract(run.definition_snapshot, '$.name') AS definition_name,
				       ROW_NUMBER() OVER (
				           PARTITION BY run.definition_id
				           ORDER BY run.admitted_at DESC, run.id DESC
				       ) AS version_rank
				FROM runs run
			)
			WHERE version_rank = 1
			ORDER BY definition_name, definition_id
		`},
		{&metrics.Repositories, `
			SELECT repository_id, repository_identity
			FROM (
				SELECT job.repository_id,
				       CASE WHEN job.repository_identity = '' THEN repository.remote_identity ELSE job.repository_identity END AS repository_identity,
				       ROW_NUMBER() OVER (
				           PARTITION BY job.repository_id
				           ORDER BY job.admitted_at DESC, job.id DESC
				       ) AS identity_rank
				FROM jobs job JOIN repositories repository ON repository.id = job.repository_id
			)
			WHERE identity_rank = 1
			ORDER BY repository_identity, repository_id
		`},
		{&metrics.Workers, `
			SELECT DISTINCT worker.id, worker.name
			FROM jobs job
			JOIN executions execution ON execution.id = job.execution_id
			JOIN workers worker ON worker.id = execution.assigned_worker_id
			ORDER BY worker.name, worker.id
		`},
	}
	for _, item := range queries {
		rows, err := query.QueryContext(ctx, item.query)
		if err != nil {
			return err
		}
		values := make([]protocol.MetricFilterOption, 0)
		for rows.Next() {
			var option protocol.MetricFilterOption
			if err := rows.Scan(&option.ID, &option.Name); err != nil {
				rows.Close()
				return err
			}
			values = append(values, option)
		}
		if err := rows.Close(); err != nil {
			return err
		}
		*item.target = values
	}
	return nil
}
