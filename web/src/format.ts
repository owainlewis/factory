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

export function runtimeLabel(runtime: string): string {
  return runtime === "claude-code" ? "Claude Code" : "Codex";
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

export interface EventSummary {
  label: string;
  text: string;
}

const hiddenEventTypes = new Set([
  "rate_limit_event",
  "system",
  "thread.started",
  "turn.started",
  "turn.completed",
]);

export function eventSummary(event: AttemptEvent): EventSummary | null {
  if (typeof event.payload === "string") {
    return { label: stateLabel(event.kind), text: event.payload };
  }
  const payload = record(event.payload);
  if (!payload) return fallbackSummary(event);

  const type = stringValue(payload.type);
  if (type === "assistant") return claudeAssistantSummary(payload);
  if (type === "user") return claudeToolResultSummary(payload);
  if (type === "result") {
    const result = stringValue(payload.result);
    return result ? { label: "Result", text: result } : null;
  }
  if (type === "item.started" || type === "item.completed") {
    return codexItemSummary(payload, type === "item.completed");
  }
  if (type === "error") {
    return { label: "Error", text: errorText(payload) ?? "The runtime reported an error." };
  }
  if (type && hiddenEventTypes.has(type)) return null;

  const stream = stringValue(payload.stream);
  const streamText = stringValue(payload.text);
  if (stream && streamText) {
    return {
      label: stream === "stderr" ? "Error output" : "Output",
      text: compactOutput(streamText, Boolean(payload.truncated)),
    };
  }
  const directText = firstString(payload, ["text", "message", "title", "summary"]);
  if (directText) {
    return { label: eventLabel(event.kind), text: directText };
  }
  if (type) return { label: "Runtime", text: humanize(type) };
  return fallbackSummary(event);
}

function claudeAssistantSummary(payload: Record<string, unknown>): EventSummary | null {
  const message = record(payload.message);
  if (!message || !Array.isArray(message.content)) return null;
  const lines: string[] = [];
  for (const value of message.content) {
    const block = record(value);
    if (!block) continue;
    if (block.type === "text") {
      const text = stringValue(block.text);
      if (text) lines.push(text);
      continue;
    }
    if (block.type === "tool_use") {
      lines.push(toolAction(block, false));
    }
  }
  return lines.length > 0 ? { label: "Assistant", text: lines.join("\n\n") } : null;
}

function claudeToolResultSummary(payload: Record<string, unknown>): EventSummary | null {
  const message = record(payload.message);
  if (!message || !Array.isArray(message.content)) return null;
  const blocks = message.content.map(record).filter((value) => value?.type === "tool_result");
  if (blocks.length === 0) return null;
  const failed = blocks.some((block) => block?.is_error === true);
  return { label: failed ? "Tool error" : "Tool", text: failed ? "Tool call failed" : "Tool call completed" };
}

function codexItemSummary(payload: Record<string, unknown>, completed: boolean): EventSummary | null {
  const item = record(payload.item);
  if (!item) return null;
  const type = stringValue(item.type);
  const failed = completed && (item.status === "failed" || (item.error !== undefined && item.error !== null));
  if (type === "agent_message") {
    const text = stringValue(item.text);
    return text ? { label: "Assistant", text } : null;
  }
  if (type === "reasoning") return null;
  if (type === "command_execution") {
    const command = compactLine(stringValue(item.command) ?? "Command");
    if (!completed) return { label: "Command", text: `Running ${command}` };
    const exitCode = numberValue(item.exit_code);
    if (!failed && exitCode === 0) return { label: "Command", text: `Succeeded: ${command}` };
    if (exitCode !== null) return { label: "Command error", text: `Failed (${exitCode}): ${command}` };
    if (failed) return { label: "Command error", text: `Failed: ${command}` };
    return { label: "Command", text: `Finished: ${command}` };
  }
  if (type?.includes("tool_call")) {
    return { label: failed ? "Tool error" : "Tool", text: toolAction(item, completed, failed) };
  }
  if (type === "file_change") {
    return {
      label: failed ? "File error" : "Files",
      text: failed ? "File changes failed" : completed ? "File changes completed" : "Changing files",
    };
  }
  if (type) return { label: "Runtime", text: `${humanize(type)} ${completed ? "completed" : "started"}` };
  return null;
}

function toolAction(value: Record<string, unknown>, completed: boolean, failed = false): string {
  const name = firstString(value, ["tool", "name"]) ?? "Tool call";
  const input = record(value.input);
  const command = input ? stringValue(input.command) : null;
  const subject = command ? `${name}: ${compactLine(command)}` : name;
  if (failed) return `${subject} failed`;
  return completed ? `${subject} completed` : `Using ${subject}`;
}

function fallbackSummary(event: AttemptEvent): EventSummary {
  return { label: eventLabel(event.kind), text: "Runtime update" };
}

function eventLabel(kind: string): string {
  if (kind === "claude-code" || kind === "codex") return "Runtime";
  return stateLabel(kind);
}

function errorText(payload: Record<string, unknown>): string | null {
  const direct = firstString(payload, ["error", "message"]);
  if (direct) return direct;
  const error = record(payload.error);
  return error ? firstString(error, ["message", "detail"]) : null;
}

function compactOutput(text: string, truncated: boolean): string {
  const trimmed = text.trim();
  if (truncated && (trimmed.startsWith("{") || trimmed.startsWith("["))) {
    return "Large structured runtime output omitted";
  }
  const maximum = 500;
  return trimmed.length > maximum ? `${trimmed.slice(0, maximum)}…` : trimmed;
}

function compactLine(value: string): string {
  const line = value.replace(/\s+/g, " ").trim();
  const maximum = 240;
  return line.length > maximum ? `${line.slice(0, maximum)}…` : line;
}

function humanize(value: string): string {
  const words = value.replace(/[._-]+/g, " ");
  return words.charAt(0).toUpperCase() + words.slice(1);
}

function record(value: unknown): Record<string, unknown> | null {
  return value !== null && typeof value === "object" && !Array.isArray(value)
    ? value as Record<string, unknown>
    : null;
}

function stringValue(value: unknown): string | null {
  return typeof value === "string" && value.trim() !== "" ? value : null;
}

function numberValue(value: unknown): number | null {
  return typeof value === "number" && Number.isFinite(value) ? value : null;
}

function firstString(value: Record<string, unknown>, keys: string[]): string | null {
  for (const key of keys) {
    const found = stringValue(value[key]);
    if (found) return found;
  }
  return null;
}
