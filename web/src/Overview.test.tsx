import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, within } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { api } from "./api";
import { OverviewView } from "./Overview";
import type { OverviewView as OverviewViewResponse } from "./types";

const overview: OverviewViewResponse = {
  active_runs: 1,
  needs_attention: 0,
  completed_last_24h: 2,
  workers_online: 1,
  workers_total: 1,
  run_metrics: {
    window: "24h",
    total_runs: 4,
    completed_runs: 2,
    completion_rate: 0.5,
    average_queue_time_seconds: 18,
    average_cycle_time_seconds: 3725,
  },
  recent_runs: [],
  upcoming_tasks: [],
  generated_at: "2026-08-13T12:00:00Z",
};

describe("OverviewView", () => {
  it("shows run performance from the overview API", async () => {
    vi.spyOn(api, "overview").mockResolvedValue(overview);
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(<QueryClientProvider client={client}>
      <OverviewView onRun={() => undefined} onTask={() => undefined} />
    </QueryClientProvider>);

    const section = await screen.findByRole("region", { name: "Run performance" });
    expect(within(section).getByText("2 completed")).toBeVisible();
    expect(metricValue(section, "Runs")).toBe("4");
    expect(metricValue(section, "Completion rate")).toBe("50%");
    expect(metricValue(section, "Average queue time")).toBe("18s");
    expect(metricValue(section, "Average cycle time")).toBe("1h 2m");
  });

  it("uses clear empty states for rates and durations", async () => {
    vi.spyOn(api, "overview").mockResolvedValue({
      ...overview,
      run_metrics: {
        ...overview.run_metrics,
        total_runs: 0,
        completed_runs: 0,
        completion_rate: null,
        average_queue_time_seconds: null,
        average_cycle_time_seconds: null,
      },
    });
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(<QueryClientProvider client={client}>
      <OverviewView onRun={() => undefined} onTask={() => undefined} />
    </QueryClientProvider>);

    const section = await screen.findByRole("region", { name: "Run performance" });
    expect(metricValue(section, "Runs")).toBe("0");
    expect(within(section).getAllByText("No data")).toHaveLength(3);
  });
});

function metricValue(section: HTMLElement, label: string): string | null {
  return within(section).getByText(label).parentElement?.querySelector("strong")?.textContent ?? null;
}
