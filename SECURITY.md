# Security Policy

Factory launches coding agents with access to repositories, developer tools,
and credentials. Treat its trust and isolation boundaries as security-sensitive.

## Supported versions

Factory is under active development and has not reached a stable release. Only
the latest commit on `main` receives security fixes.

## Reporting a vulnerability

Do not open a public issue for a suspected vulnerability. Email
[owain@owainlewis.com](mailto:owain@owainlewis.com) with:

- a clear description of the issue and its impact;
- the affected revision and configuration;
- reproduction steps or a proof of concept; and
- any suggested mitigation, if known.

Do not include real credentials or private repository data. You should receive
an acknowledgement within seven days. Once the report is understood, the
maintainer will coordinate remediation and disclosure with you. Please allow a
reasonable period for a fix before publishing details.

## Security model

A managed worktree protects the canonical checkout, but it is not a security
boundary. The worker still shares the host, network, processes, and credentials.
Use Docker execution for stronger local isolation, narrow credentials, trusted
ticket authors, and protected branches. See the
[operations guide](docs/operations.md) for deployment guidance.
