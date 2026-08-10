# GitHub webhook Automations

A GitHub webhook Automation connects pull-request events to a shared
Definition. Factory verifies and deduplicates the delivery, then creates an
ordinary Run for the configured repository. The agent receives bounded event
context and uses its authenticated `gh` CLI for review comments or other
GitHub work.

## Configure the public listener

Generate a random webhook secret and store it in an owner-only file:

```sh
umask 077
openssl rand -hex 32 > /etc/factory/github-webhook-secret
```

Add a separate TLS listener to `~/.factory/config.toml`:

```toml
webhook_listen = "0.0.0.0:7444"
webhook_tls_cert = "/etc/factory/tls/server.crt"
webhook_tls_key = "/etc/factory/tls/server.key"
github_webhook_secret_file = "/etc/factory/github-webhook-secret"
```

All four values are required together. This listener exposes only `GET
/healthz` and `POST /api/v1/webhooks/github`. The browser, operator, Worker,
repository, and Run APIs remain unavailable on it.

In the GitHub repository settings, add this payload URL:

```text
https://factory.example.com:7444/api/v1/webhooks/github
```

Choose `application/json`, enter the same secret, enable SSL verification, and
subscribe to pull request events.

## Create the Automation

1. Create a Definition whose allowed tools include `gh`.
2. Configure and enable the GitHub repository in Factory.
3. Open **Automations**, choose **GitHub webhook**, then select the Definition
   and repository.
4. Enable the Automation.

Pull request `opened` and `synchronize` deliveries now create Runs. The
Automation occurrence and Run record show the delivery ID, event, pull-request
number, URL, and observed head commit. Replaying the same valid delivery does
not create another occurrence or Run. A reused delivery ID with different
payload bytes is rejected.

Invalid signatures are rejected before payload parsing and are not stored.
Failures after a signed delivery matches an Automation appear on that
Automation's occurrence and health status so the delivery can be retried.

Factory does not post deterministic GitHub comments. The Definition tells the
coding agent what to do, and the agent re-fetches live pull-request state with
`gh` before it acts.
