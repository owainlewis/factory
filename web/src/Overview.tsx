import { useQuery } from "@tanstack/react-query";
import { Gauge, Users } from "lucide-react";
import { useState } from "react";
import { api } from "./api";
import { duration, timeAgo } from "./format";
import { useVisibleInterval } from "./polling";
import type { JobState, MetricsFilters, MetricsSummary, MetricsWindow, RunMetricJob } from "./types";
import { ErrorState, LoadingState, PanelHeading, StaleBanner, StatusBadge, ViewHeader } from "./ui";

const windows: Array<{ value: MetricsWindow; label: string }> = [
  { value: "24h", label: "24 hours" },
  { value: "7d", label: "7 days" },
  { value: "30d", label: "30 days" },
  { value: "all", label: "All retained" },
];

type JobView = "all" | "active" | "blocked" | "succeeded" | "failed" | "finished" | "started" | "terminal";

export function Overview({ onRun }: { onRun: (id: string, jobID?: string) => void }) {
  const [window, setWindow] = useState<MetricsWindow>("7d");
  const [filters, setFilters] = useState<MetricsFilters>({});
  const [jobView, setJobView] = useState<JobView>("all");
  const interval = useVisibleInterval(10_000);
  const metrics = useQuery({
    queryKey: ["metrics", window, filters, jobView],
    queryFn: () => api.metrics(window, filters, jobView),
    refetchInterval: interval,
  });

  if (metrics.isPending) return <LoadingState label="Loading Run health" />;
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
        <span>Job admission window</span>
        <div className="window-picker" aria-label="Metrics window">
          {windows.map((option) => <button
            key={option.value}
            aria-pressed={window === option.value}
            onClick={() => setWindow(option.value)}
          >{option.label}</button>)}
        </div>
      </div>
      {metrics.data && <RunMetrics
        data={metrics.data}
        filters={filters}
        onFilters={setFilters}
        jobView={jobView}
        onJobView={setJobView}
        onRun={onRun}
      />}
    </div>
  );
}

function RunMetrics({
  data,
  filters,
  onFilters,
  jobView,
  onJobView,
  onRun,
}: {
  data: MetricsSummary;
  filters: MetricsFilters;
  onFilters: (filters: MetricsFilters) => void;
  jobView: JobView;
  onJobView: (view: JobView) => void;
  onRun: (id: string, jobID?: string) => void;
}) {
  const health = data.run_health;
  const cards: Array<{ label: string; value: string; detail: string; view: JobView }> = [
    { label: "Active Jobs", value: formatNumber(health.active), detail: "Queued, preparing, or running", view: "active" },
    { label: "Blocked Jobs", value: formatNumber(health.blocked), detail: "Waiting for capacity or a Runner", view: "blocked" },
    { label: "Succeeded Jobs", value: formatNumber(health.succeeded), detail: "Terminal successful Jobs", view: "succeeded" },
    { label: "Failed Jobs", value: formatNumber(health.failed), detail: "Terminal failed Jobs", view: "failed" },
    { label: "Success rate", value: formatRate(health.success_rate), detail: "Succeeded ÷ succeeded and failed", view: "finished" },
    { label: "Average queue time", value: formatSeconds(health.average_queue_time_seconds), detail: "Admission to first agent start", view: "started" },
    { label: "Average cycle time", value: formatSeconds(health.average_cycle_time_seconds), detail: "Admission to terminal outcome", view: "terminal" },
    { label: "Throughput", value: formatNumber(health.throughput), detail: "Terminal Jobs in this cohort", view: "terminal" },
  ];
  const visibleJobs = health.jobs.filter((job) => jobMatchesView(job, jobView));
  return (
    <section className="metrics-dashboard" aria-label="Run health metrics">
      <div className="metrics-filters" aria-label="Run metric filters">
        <MetricSelect label="Definition" value={filters.definition_id ?? ""} options={health.definitions} onChange={(value) => onFilters({ ...filters, definition_id: value || undefined })} />
        <MetricSelect label="Repository" value={filters.repository_id ?? ""} options={health.repositories} onChange={(value) => onFilters({ ...filters, repository_id: value || undefined })} />
        <MetricSelect label="Runner" value={filters.runner_id ?? ""} options={health.runners} onChange={(value) => onFilters({ ...filters, runner_id: value || undefined })} />
        <button className="button button-secondary" disabled={Object.keys(filters).length === 0} onClick={() => onFilters({})}>Clear filters</button>
      </div>

      <div className="primary-metrics run-primary-metrics">
        {cards.map((metric) => <button
          type="button"
          className="metric-card"
          key={metric.label}
          aria-pressed={jobView === metric.view}
          onClick={() => onJobView(metric.view)}
        >
          <span className="metric-label">{metric.label}</span>
          <strong>{metric.value}</strong>
          <small>{metric.detail}</small>
        </button>)}
      </div>

      <section className="panel run-health-jobs">
        <PanelHeading title={jobView === "all" ? "Jobs in this view" : `${jobViewLabel(jobView)} Jobs`} aside={`${visibleJobs.length} shown · ${health.total_jobs} total`} />
        {jobView !== "all" && <button className="button button-secondary" onClick={() => onJobView("all")}>Show all Jobs</button>}
        {visibleJobs.length === 0 ? <div className="quiet-empty">No Jobs match this metric and filter set.</div> : <div className="run-health-job-list">
          {visibleJobs.map((job) => <button key={job.job_id} className="run-health-job" onClick={() => onRun(job.run_id, job.job_id)}>
            <span><strong>{job.repository_remote_identity}</strong><small>{job.definition_name} · {job.runner_name || "No Runner assigned"}</small></span>
            <StatusBadge state={job.state} />
            <span className="mono muted">{job.terminal_at ? duration(job.admitted_at, job.terminal_at) : timeAgo(job.admitted_at)}</span>
          </button>)}
        </div>}
      </section>

      <section className="metric-panel runner-health-summary" aria-label="Runner health summary">
        <div><Gauge size={15} /><span>Codex weekly limit</span><strong>{data.weekly_limit ? `${100 - data.weekly_limit.used_percent}% left` : "Unavailable"}</strong></div>
        <div><Users size={15} /><span>Runners online</span><strong>{data.workers_online} / {data.workers_total}</strong></div>
      </section>
      <details className="metrics-formulas">
        <summary>Metric formulas</summary>
        <p>Every metric uses the same cohort: Jobs admitted in the selected window after the current filters are applied. Active is queued, preparing, or running. Success rate excludes cancellations. Queue time ends at the first agent start. Cycle time ends at the terminal outcome. Throughput counts terminal Jobs in the cohort.</p>
      </details>
    </section>
  );
}

function MetricSelect({ label, value, options, onChange }: {
  label: string;
  value: string;
  options: Array<{ id: string; name: string }>;
  onChange: (value: string) => void;
}) {
  return <label><span>{label}</span><select aria-label={`${label} filter`} value={value} onChange={(event) => onChange(event.target.value)}><option value="">All</option>{options.map((option) => <option key={option.id} value={option.id}>{option.name}</option>)}</select></label>;
}

function jobMatchesView(job: RunMetricJob, view: JobView): boolean {
  const active: JobState[] = ["queued", "preparing", "running"];
  if (view === "all") return true;
  if (view === "active") return active.includes(job.state);
  if (view === "finished") return job.state === "succeeded" || job.state === "failed";
  if (view === "started") return Boolean(job.started_at);
  if (view === "terminal") return Boolean(job.terminal_at);
  return job.state === view;
}

function jobViewLabel(view: JobView): string {
  if (view === "finished") return "Succeeded and failed";
  if (view === "started") return "Started";
  if (view === "terminal") return "Terminal";
  return view[0].toUpperCase() + view.slice(1);
}

function formatNumber(value: number): string {
  return new Intl.NumberFormat().format(value);
}

function formatRate(value: number | null): string {
  if (value === null) return "Not enough data";
  return new Intl.NumberFormat(undefined, { style: "percent", maximumFractionDigits: 1 }).format(value);
}

function formatSeconds(value: number | null): string {
  if (value === null) return "Not enough data";
  const seconds = Math.max(0, Math.round(value));
  if (seconds < 60) return `${seconds}s`;
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return `${minutes}m ${seconds % 60}s`;
  return `${Math.floor(minutes / 60)}h ${minutes % 60}m`;
}
