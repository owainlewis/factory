import { useQuery } from "@tanstack/react-query";
import { CircleDot, Play, RotateCcw, Users } from "lucide-react";
import { useState, type ReactNode } from "react";
import { api } from "./api";
import { useVisibleInterval } from "./polling";
import type { MetricsSummary, MetricsWindow } from "./types";
import { ErrorState, LoadingState, StaleBanner, ViewHeader } from "./ui";

const windows: Array<{ value: MetricsWindow; label: string }> = [
  { value: "24h", label: "24 hours" },
  { value: "7d", label: "7 days" },
  { value: "30d", label: "30 days" },
  { value: "all", label: "All retained" },
];

export function Overview() {
  const [window, setWindow] = useState<MetricsWindow>("7d");
  const interval = useVisibleInterval(10_000);
  const metrics = useQuery({
    queryKey: ["metrics", window],
    queryFn: () => api.metrics(window),
    refetchInterval: interval,
  });

  if (metrics.isPending) return <LoadingState label="Loading Factory metrics" />;
  if (metrics.error && !metrics.data) {
    return <ErrorState error={metrics.error} onRetry={() => void metrics.refetch()} />;
  }

  return (
    <div className="page page-overview">
      <ViewHeader
        title="Factory overview"
        fetching={metrics.isFetching}
        updatedAt={metrics.dataUpdatedAt}
        onRefresh={() => void metrics.refetch()}
      />
      {metrics.error && <StaleBanner error={metrics.error} />}
      <div className="metrics-toolbar">
        <span>Time window</span>
        <div className="window-picker" aria-label="Metrics window">
          {windows.map((option) => (
            <button
              key={option.value}
              aria-pressed={window === option.value}
              onClick={() => setWindow(option.value)}
            >
              {option.label}
            </button>
          ))}
        </div>
      </div>
      {metrics.data && <Metrics data={metrics.data} />}
    </div>
  );
}

function Metrics({ data }: { data: MetricsSummary }) {
  const primary = [
    {
      label: "Executions created",
      value: formatNumber(data.executions_created),
      detail: "New work entering Factory",
    },
    {
      label: "Executions completed",
      value: formatNumber(data.executions_completed),
      detail: "Succeeded, failed, or cancelled",
    },
    {
      label: "Success rate",
      value: formatRate(data.success_rate),
      detail: "Succeeded out of succeeded and failed",
    },
    {
      label: "Median cycle time",
      value: formatSeconds(data.median_cycle_time_seconds),
      detail: "Creation to succeeded or failed",
    },
    {
      label: "Weekly limit",
      value: data.weekly_limit ? `${100 - data.weekly_limit.used_percent}% left` : "Unavailable",
      detail: data.weekly_limit
        ? formatReset(data.weekly_limit.resets_at)
        : "Requires an online Codex worker",
      remaining: data.weekly_limit ? 100 - data.weekly_limit.used_percent : null,
    },
  ];

  return (
    <section className="metrics-dashboard" aria-label="Factory metrics">
      <div className="primary-metrics">
        {primary.map((metric) => (
          <article className="metric-card" key={metric.label}>
            <span className="metric-label">{metric.label}</span>
            <strong>{metric.value}</strong>
            {metric.remaining !== undefined && metric.remaining !== null && (
              <meter
                aria-label="Weekly limit remaining"
                min="0"
                max="100"
                value={metric.remaining}
              />
            )}
            <small>{metric.detail}</small>
          </article>
        ))}
      </div>

      <div className="metrics-detail-grid">
        <section className="metric-panel" aria-labelledby="outcomes-heading">
          <div className="metric-panel-heading">
            <h2 id="outcomes-heading">Execution outcomes</h2>
            <span>{formatNumber(data.executions_completed)} total</span>
          </div>
          <div className="outcome-bars">
            <Outcome
              label="Succeeded"
              count={data.succeeded}
              total={data.executions_completed}
              tone="succeeded"
            />
            <Outcome
              label="Failed"
              count={data.failed}
              total={data.executions_completed}
              tone="failed"
            />
            <Outcome
              label="Cancelled"
              count={data.cancelled}
              total={data.executions_completed}
              tone="cancelled"
            />
          </div>
        </section>

        <section className="metric-panel" aria-labelledby="health-heading">
          <div className="metric-panel-heading">
            <h2 id="health-heading">Factory health</h2>
            <span>Now</span>
          </div>
          <dl className="health-metrics">
            <HealthMetric icon={<CircleDot size={15} />} label="Queued now" value={data.queued} />
            <HealthMetric icon={<Play size={15} />} label="Running now" value={data.running} />
            <HealthMetric
              icon={<RotateCcw size={15} />}
              label="Retry rate"
              value={formatRate(data.retry_rate)}
            />
            <HealthMetric
              icon={<Users size={15} />}
              label="Workers online"
              value={`${data.workers_online} / ${data.workers_total}`}
            />
          </dl>
        </section>
      </div>

      <p className="metrics-note">
        Rates exclude cancellations. Retained control-plane data only.
      </p>
    </section>
  );
}

function Outcome({
  label,
  count,
  total,
  tone,
}: {
  label: string;
  count: number;
  total: number;
  tone: "succeeded" | "failed" | "cancelled";
}) {
  const width = total === 0 ? 0 : Math.round((count / total) * 100);
  return (
    <div className="outcome-row">
      <span>{label}</span>
      <div className="outcome-track" aria-hidden="true">
        <span className={`outcome-fill outcome-${tone}`} style={{ width: `${width}%` }} />
      </div>
      <strong>{formatNumber(count)}</strong>
    </div>
  );
}

function HealthMetric({
  icon,
  label,
  value,
}: {
  icon: ReactNode;
  label: string;
  value: number | string;
}) {
  return (
    <div>
      <dt><span aria-hidden="true">{icon}</span>{label}</dt>
      <dd>{typeof value === "number" ? formatNumber(value) : value}</dd>
    </div>
  );
}

function formatNumber(value: number): string {
  return new Intl.NumberFormat().format(value);
}

function formatRate(value: number | null): string {
  if (value === null) return "Not enough data";
  return new Intl.NumberFormat(undefined, {
    style: "percent",
    maximumFractionDigits: 1,
  }).format(value);
}

function formatSeconds(value: number | null): string {
  if (value === null) return "Not enough data";
  const seconds = Math.max(0, Math.round(value));
  if (seconds < 60) return `${seconds}s`;
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return `${minutes}m ${seconds % 60}s`;
  const hours = Math.floor(minutes / 60);
  return `${hours}h ${minutes % 60}m`;
}

function formatReset(value: string): string {
  const reset = new Date(value);
  const time = new Intl.DateTimeFormat(undefined, {
    hour: "2-digit",
    minute: "2-digit",
  }).format(reset);
  const date = new Intl.DateTimeFormat(undefined, {
    day: "numeric",
    month: "short",
  }).format(reset);
  return `Resets ${time} on ${date}`;
}
