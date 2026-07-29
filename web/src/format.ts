import type { AttemptEvent, TaskState } from "./types";

export const taskStates: TaskState[] = [
  "queued",
  "running",
  "succeeded",
  "failed",
  "cancelled",
];

export function stateLabel(state: string): string {
  return state.charAt(0).toUpperCase() + state.slice(1);
}

export function timeAgo(value: string, now = Date.now()): string {
  const seconds = Math.max(0, Math.floor((now - new Date(value).getTime()) / 1000));
  if (seconds < 10) return "just now";
  if (seconds < 60) return `${seconds}s ago`;
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return `${minutes}m ago`;
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return `${hours}h ago`;
  return `${Math.floor(hours / 24)}d ago`;
}

export function duration(start: string, end?: string, now = Date.now()): string {
  const elapsed = Math.max(0, (end ? new Date(end).getTime() : now) - new Date(start).getTime());
  const seconds = Math.floor(elapsed / 1000);
  if (seconds < 60) return `${seconds}s`;
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return `${minutes}m ${seconds % 60}s`;
  const hours = Math.floor(minutes / 60);
  return `${hours}h ${minutes % 60}m`;
}

export function eventText(event: AttemptEvent): string {
  if (typeof event.payload === "string") return event.payload;
  if (event.payload && typeof event.payload === "object") {
    const payload = event.payload as Record<string, unknown>;
    for (const key of ["text", "message", "title", "summary"]) {
      if (typeof payload[key] === "string") return payload[key];
    }
    return JSON.stringify(payload);
  }
  return String(event.payload ?? "");
}
