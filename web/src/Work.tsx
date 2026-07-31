import { ChevronRight, ListChecks, Plus } from "lucide-react";
import { runtimeLabel, stateLabel, taskStates, timeAgo } from "./format";
import type { Task, TaskState, Worker } from "./types";
import {
  EmptyState,
  ErrorState,
  LoadingState,
  StaleBanner,
  StatusBadge,
  ViewHeader,
  type ViewStateProps,
} from "./ui";

export function WorkView({
  tasks,
  workers,
  pending,
  error,
  fetching,
  updatedAt,
  onTask,
  onDelegate,
  onRefresh,
  hasMore,
  loadingMore,
  onLoadMore,
}: ViewStateProps & {
  tasks?: Task[];
  workers?: Worker[];
  pending: boolean;
  error: Error | null;
  onTask: (id: string) => void;
  onDelegate: () => void;
  hasMore: boolean;
  loadingMore: boolean;
  onLoadMore: () => void;
}) {
  if (pending) return <LoadingState label="Loading work" />;
  if (error && !tasks) return <ErrorState error={error} onRetry={onRefresh} />;

  const grouped = Object.fromEntries(
    taskStates.map((state) => [state, (tasks ?? []).filter((task) => task.state === state)]),
  ) as Record<TaskState, Task[]>;
  const workerMap = new Map((workers ?? []).map((worker) => [worker.id, worker]));

  return (
    <div className="page page-work">
      <ViewHeader
        title="Agent work"
        fetching={fetching}
        updatedAt={updatedAt}
        onRefresh={onRefresh}
      />
      {error && <StaleBanner error={error} />}
      {(tasks ?? []).length === 0 ? (
        <EmptyState
          icon={<ListChecks size={22} />}
          title="No work yet"
          description="Delegate the first task to a registered worker. It will stay here through restarts."
          action={<button className="button button-primary" onClick={onDelegate}><Plus size={16} /> Delegate task</button>}
        />
      ) : (
        <div className="work-board" data-testid="work-board">
          {taskStates.map((state) => (
            <section className="work-column" key={state} aria-labelledby={`heading-${state}`}>
              <div className="column-heading">
                <span className={`status-dot status-${state}`} aria-hidden="true" />
                <h2 id={`heading-${state}`}>{stateLabel(state)}</h2>
                <span className="count">{grouped[state].length}</span>
              </div>
              <div className="task-stack">
                {grouped[state].length === 0 ? (
                  <div className="column-empty">No {state} work</div>
                ) : (
                  grouped[state].map((task) => (
                    <TaskCard
                      key={task.id}
                      task={task}
                      worker={workerMap.get(task.worker_id)}
                      onClick={() => onTask(task.id)}
                    />
                  ))
                )}
              </div>
            </section>
          ))}
        </div>
      )}
      {hasMore && (
        <div className="load-more">
          <button
            className="button button-secondary"
            onClick={onLoadMore}
            disabled={loadingMore}
          >
            {loadingMore ? "Loading more…" : "Load more work"}
          </button>
        </div>
      )}
    </div>
  );
}

function TaskCard({ task, worker, onClick }: { task: Task; worker?: Worker; onClick: () => void }) {
  return (
    <button className="task-card" onClick={onClick}>
      <div className="task-card-top">
        <StatusBadge state={task.state} />
        <ChevronRight size={14} aria-hidden="true" />
      </div>
      <span className="task-title">{task.title}</span>
      {task.description && <span className="task-description">{task.description}</span>}
      <div className="task-meta">
        <span className="task-worker">{worker?.name ?? "Unknown worker"}</span>
        <span aria-hidden="true">·</span>
        {worker && (
          <>
            <span>{runtimeLabel(worker.runtime)}</span>
            <span aria-hidden="true">·</span>
          </>
        )}
        <span>{timeAgo(task.created_at)}</span>
      </div>
    </button>
  );
}
