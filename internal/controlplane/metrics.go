package controlplane

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/owainlewis/factory/internal/protocol"
)

const (
	metricsWindow24Hours = "24h"
	metricsWindow7Days   = "7d"
	metricsWindow30Days  = "30d"
	metricsWindowAll     = "all"
)

func parseMetricsWindow(value string) (string, time.Duration, error) {
	switch value {
	case "", metricsWindow7Days:
		return metricsWindow7Days, 7 * 24 * time.Hour, nil
	case metricsWindow24Hours:
		return metricsWindow24Hours, 24 * time.Hour, nil
	case metricsWindow30Days:
		return metricsWindow30Days, 30 * 24 * time.Hour, nil
	case metricsWindowAll:
		return metricsWindowAll, 0, nil
	default:
		return "", 0, invalid(
			"invalid_window",
			"window must be one of 24h, 7d, 30d, or all",
		)
	}
}

func (s *Store) Metrics(ctx context.Context, window string) (protocol.MetricsSummary, error) {
	window, duration, err := parseMetricsWindow(window)
	if err != nil {
		return protocol.MetricsSummary{}, err
	}
	now := s.now().UTC()
	var start *time.Time
	if duration > 0 {
		value := now.Add(-duration)
		start = &value
	}
	summary := protocol.MetricsSummary{Window: window, GeneratedAt: now}

	if err := s.countCreatedExecutions(ctx, start, now, &summary.ExecutionsCreated); err != nil {
		return protocol.MetricsSummary{}, unavailable(err)
	}
	if err := s.countExecutionOutcomes(ctx, start, now, &summary); err != nil {
		return protocol.MetricsSummary{}, unavailable(err)
	}
	if err := s.countCurrentExecutions(ctx, &summary); err != nil {
		return protocol.MetricsSummary{}, unavailable(err)
	}
	if err := s.calculateOutcomeRates(ctx, start, now, &summary); err != nil {
		return protocol.MetricsSummary{}, unavailable(err)
	}
	if err := s.countWorkers(ctx, now, &summary); err != nil {
		return protocol.MetricsSummary{}, unavailable(err)
	}
	return summary, nil
}

func metricsTimeFilter(column string, start *time.Time, end time.Time) (string, []any) {
	if start == nil {
		return fmt.Sprintf("%s <= ?", column), []any{end.UnixMilli()}
	}
	return fmt.Sprintf("%s >= ? AND %s <= ?", column, column),
		[]any{start.UnixMilli(), end.UnixMilli()}
}

func (s *Store) countCreatedExecutions(
	ctx context.Context,
	start *time.Time,
	end time.Time,
	count *int64,
) error {
	filter, args := metricsTimeFilter("created_at", start, end)
	return s.db.QueryRowContext(
		ctx,
		`SELECT COUNT(*) FROM executions WHERE `+filter,
		args...,
	).Scan(count)
}

func (s *Store) countExecutionOutcomes(
	ctx context.Context,
	start *time.Time,
	end time.Time,
	summary *protocol.MetricsSummary,
) error {
	filter, args := metricsTimeFilter("updated_at", start, end)
	rows, err := s.db.QueryContext(ctx, `
		SELECT state, COUNT(*)
		FROM executions
		WHERE state IN ('succeeded', 'failed', 'cancelled') AND `+filter+`
		GROUP BY state
	`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var state string
		var count int64
		if err := rows.Scan(&state, &count); err != nil {
			return err
		}
		switch state {
		case "succeeded":
			summary.Succeeded = count
		case "failed":
			summary.Failed = count
		case "cancelled":
			summary.Cancelled = count
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	summary.ExecutionsCompleted = summary.Succeeded + summary.Failed + summary.Cancelled
	return nil
}

func (s *Store) countCurrentExecutions(
	ctx context.Context,
	summary *protocol.MetricsSummary,
) error {
	rows, err := s.db.QueryContext(ctx, `
		SELECT state, COUNT(*)
		FROM executions
		WHERE state IN ('queued', 'preparing', 'running')
		GROUP BY state
	`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var state string
		var count int64
		if err := rows.Scan(&state, &count); err != nil {
			return err
		}
		if state == "queued" {
			summary.Queued = count
		} else {
			summary.Running += count
		}
	}
	return rows.Err()
}

func (s *Store) calculateOutcomeRates(
	ctx context.Context,
	start *time.Time,
	end time.Time,
	summary *protocol.MetricsSummary,
) error {
	finished := summary.Succeeded + summary.Failed
	if finished == 0 {
		return nil
	}
	successRate := float64(summary.Succeeded) / float64(finished)
	summary.SuccessRate = &successRate

	filter, args := metricsTimeFilter("e.updated_at", start, end)
	var retried int64
	err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM (
			SELECT e.id
			FROM executions e
			JOIN attempts a ON a.execution_id = e.id
			WHERE e.state IN ('succeeded', 'failed') AND `+filter+`
			GROUP BY e.id
			HAVING COUNT(a.id) > 1
		)
	`, args...).Scan(&retried)
	if err != nil {
		return err
	}
	retryRate := float64(retried) / float64(finished)
	summary.RetryRate = &retryRate

	offset := (finished - 1) / 2
	limit := int64(1)
	if finished%2 == 0 {
		limit = 2
	}
	var medianMillis sql.NullFloat64
	medianArgs := append(append([]any{}, args...), limit, offset)
	err = s.db.QueryRowContext(ctx, `
		SELECT AVG(duration_millis)
		FROM (
			SELECT MAX(0, e.updated_at - e.created_at) AS duration_millis
			FROM executions e
			WHERE e.state IN ('succeeded', 'failed') AND `+filter+`
			ORDER BY duration_millis
			LIMIT ? OFFSET ?
		)
	`, medianArgs...).Scan(&medianMillis)
	if err != nil {
		return err
	}
	if medianMillis.Valid {
		seconds := medianMillis.Float64 / float64(time.Second/time.Millisecond)
		summary.MedianCycleTimeSeconds = &seconds
	}
	return nil
}

func (s *Store) countWorkers(
	ctx context.Context,
	now time.Time,
	summary *protocol.MetricsSummary,
) error {
	cutoff := now.Add(-protocol.WorkerOnlineWindow).UnixMilli()
	return s.db.QueryRowContext(ctx, `
		SELECT COUNT(*), COALESCE(SUM(
			CASE WHEN last_heartbeat >= ? AND last_heartbeat <= ? THEN 1 ELSE 0 END
		), 0)
		FROM workers
	`, cutoff, now.UnixMilli()).Scan(&summary.WorkersTotal, &summary.WorkersOnline)
}
