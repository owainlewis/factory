# Poll issue queues

Factory can poll issue trackers and delegate matching tickets as normal tasks.
The poller is a separate Go process. The control plane stays focused on durable
task coordination, and workers stay focused on agent execution.

## GitHub quick start

> **GitHub dependency:** Factory polls GitHub by running the `gh` CLI. It does
> not call the GitHub API directly and will not start a GitHub queue when `gh`
> is missing. Install `gh` from [cli.github.com](https://cli.github.com/).

Requirements:

- a running Factory control plane;
- a healthy online worker that advertises the target repository and opts into
  GitHub source access;
- the authenticated `gh` CLI on the poller host;
- the authenticated `gh` CLI on the worker host when the agent workflow reads
  or updates GitHub.

Confirm the poller host is ready:

```sh
gh --version
gh auth status
```

If authentication is missing, run:

```sh
gh auth login
```

Build the binaries and copy the example:

```sh
just build
cp examples/poller.toml ~/.factory/poller.toml
```

Edit the queue:

```toml
[[queues]]
name = "github-ready"
source = "github"
project = "owner/repository"
status = "open"
labels = ["factory:ready"]
prompt = """
Work on this ticket end to end.
Use gh to read the live issue before changing code.
Implement and verify the change, update the issue, and open a pull request.
"""
timeout_seconds = 7200
```

Opt each eligible worker into GitHub routing once in `worker.toml`:

```toml
source_access = ["github"]
```

The worker advertises GitHub access only while its local
`gh auth status --hostname github.com` probe succeeds.

Safely test GitHub matching before creating a task:

```sh
just poll-test ~/.factory/poller.toml github-ready
```

This prints normalized matching issue references as JSON. It does not contact
the control plane, create a task, or read or write the poller ledger. It never
prints an issue body.

Submit one real pass:

```sh
just poll-once ~/.factory/poller.toml
```

Run continuously:

```sh
just poll ~/.factory/poller.toml
```

The built binary works without the source checkout:

```sh
~/.factory/bin/factory-poller \
  -config ~/.factory/poller.toml
```

## Queue contract

Each `[[queues]]` entry configures:

- a stable queue name;
- a source name and project;
- one native status and zero or more required labels;
- the trusted prompt placed before ticket context;
- an optional timeout.

A GitHub queue contains no worker ID or repository key. The project identifies
the repository. Non-GitHub command queues retain their explicit worker and
repository fields until their routing adapters adopt the same source-access
contract.

The built-in GitHub source runs:

```text
gh issue list --repo PROJECT --state STATUS --label LABEL ...
```

It reads at most 100 issues. A larger result fails the pass so Factory does not
mistake a truncated list for a complete queue.

The task title is `Work on <source> ticket <key>`. Ticket keys are restricted to
identifier characters, and the provider-controlled ticket title stays inside
the task description. GitHub polling reads and stores only the issue number,
URL, title, state, and labels. It never requests the issue body. The task prompt
contains those minimal fields and tells the agent to fetch the live issue,
revalidate the configured state and labels, and stop without mutation when the
condition is stale.

The poller does not mutate issues. Updating labels, status, comments, branches,
or pull requests is part of the configured agent prompt.

## Jira, Linear, and other CLIs

Factory does not include provider SDKs. A non-GitHub queue names the provider
and configures one executable:

```toml
[[queues]]
name = "jira-ready"
source = "jira"
command = ["factory-source-jira"]
project = "ENG"
status = "Ready for Development"
labels = ["factory"]
worker_id = "61b30338-95dc-4704-80bd-8a4c63aa3037"
repository_key = "service"
prompt = "Work on this Jira ticket using jiractrl."
```

Factory invokes the command without a shell:

```text
factory-source-jira \
  --project "ENG" \
  --status "Ready for Development" \
  --label "factory"
```

The command uses its provider CLI and prints one strict JSON value:

```json
{
  "issues": [
    {
      "key": "ENG-123",
      "title": "Repair queue admission",
      "description": "Ticket body",
      "state": "Ready for Development",
      "labels": ["factory"],
      "url": "https://jira.example/browse/ENG-123"
    }
  ]
}
```

The same contract works for a Linear CLI. The adapter owns authentication and
output normalization. Factory passes only project, status, and labels, never a
credential. Install the provider CLI separately on a worker when its agent
workflow must read or update the live ticket. Output is limited to 4 MiB,
stderr to 64 KiB, execution to 30 seconds, and each result to 100 issues.

## Delivery and deduplication

For GitHub tasks, the poller sends the repository remote and required GitHub
source access to the control plane instead of a worker ID. In the task-creation
transaction, the control plane considers healthy online workers that advertise
both. It chooses the lowest `(active + queued) / capacity` load and breaks an
exact tie by worker ID. The selected worker and repository are then frozen on
the task and execution.

The poller stores its ledger in
`~/.factory/poller/poller.sqlite3` by default. It writes the exact task request
before posting it. A failed or lost response is retried with the same request
key, so control-plane idempotency returns the original task.

An issue is dispatched once for the same queue name, source, project, and issue
key. Repeated polls and process restarts do not create duplicates. The MVP does
not rearm an issue after it leaves and re-enters the configured condition.
Changing the queue name or project creates a new queue identity and may dispatch
matching issues again.

The ledger holds at most 10,000 observations. Once a task is submitted, its
stored request body is cleared and only the deduplication identity and task ID
remain. At the limit, new dispatches fail with an operator error. Stop the
poller and archive its database before resetting the ledger, then check the
live queue to avoid redispatching old issues.

## Failure behavior

- One failed queue does not stop other queues in the same pass.
- A failed or malformed source result creates no new observations.
- A failed task submission remains pending and is recovered before the next
  source poll.
- When no worker currently advertises both the repository and GitHub access,
  the pass fails without retaining a stale pending request. The next pass
  refetches and revalidates the issue before trying to route it again.
- Oversized prompts and source results fail instead of being truncated.
- A full 10,000-row dispatch ledger rejects new observations.
- Configuration is read at startup. Restart the poller after changing it.
- Ctrl+C or SIGTERM stops before another polling pass begins.

Poller state contains issue references and titles, task requests, repository
names, and task IDs. GitHub issue bodies are not retained. Protect
`~/.factory` as sensitive local data.
