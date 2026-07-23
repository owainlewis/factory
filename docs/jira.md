# Use Jira as a source

Factory v1 officially supports one GitHub source. This repository also includes
a Jira adapter backed by `jiractrl` and `jq` to demonstrate the source adapter
contract. The adapter asks Jira for only issues matching the trigger's exact
state and labels. As with the GitHub adapter, it does not filter by author —
restrict trust to people who can label issues in the target Jira project.

## Authenticate

Configure `jiractrl` first:

```sh
export JIRACTRL_BASE_URL="https://jira.example.com"
export JIRACTRL_TOKEN="..."
jiractrl auth check
```

Use a dedicated, revocable credential. Do not commit it to the repository or
put it in issue content, logs, or workflow files.

## Configure the adapter

Replace the source and workflow paths in `.factory/config.toml`. Adapt the
project key, state names, and label to your Jira project:

```toml
[source]
command = [
  ".factory/sources/jira",
  "--project", "SPS",
  "--max-results", "100",
]

[trigger.triage]
type = "source"
state = "Ready For Spec"
labels = ["factory-ready"]
workflow = ".factory/workflows/jira/triage/WORKFLOW.md"

[trigger.implement]
type = "source"
state = "Ready To Implement"
labels = ["factory-ready"]
workflow = ".factory/workflows/jira/implement/WORKFLOW.md"
timeout = "4h"
```

The adapter builds bounded JQL such as:

```text
project = "SPS" AND status = "Ready To Implement" AND labels = "factory-ready"
```

Factory passes only the Jira key, such as `SPS-123`, to the worker. The Jira
workflow tells the agent to fetch, comment, update, and transition the live
ticket with `jiractrl`; `git` and `gh` remain responsible for code and pull
requests.

## Worker requirements

The included Jira example is configured with `sandbox = "worktree"`, so the
worker inherits the host's `jiractrl` binary and Jira environment variables. A
Docker worker would also need `jiractrl` in its image and an explicit Jira
credential mount or environment policy. That environment is not included in
this example.

Read the [runnable guide](local-v1.md) for the complete Factory configuration
and the [source adapter contract](local-v1.md#source-adapter-contract) that the
Jira script implements.
