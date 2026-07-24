# Ticket classification

Factory stores three independent ticket dimensions:

- type classifies the work;
- priority ranks accepted work;
- status records lifecycle.

`.factory/tickets.toml` defines whether each dimension uses labels or a
single-select project field. Do not represent the same fact in more than one
place.

Status storage and Factory dispatch must use the same backend.
`.factory/tickets.toml` tells workflows how to move a ticket, while the source
and triggers in `.factory/config.toml` tell Factory which tickets are eligible.
When changing status storage, update both files together and run
`factory validate`.

## Type

Every open ticket has exactly one type value. This repository stores type as a
label:

| Label | Meaning |
| --- | --- |
| `bug` | Existing behaviour is incorrect. |
| `enhancement` | New capability or an improvement to existing behaviour. |
| `documentation` | Documentation is the primary deliverable. |

Features are enhancements. Do not add a separate `feature` label.

Suspected vulnerabilities are reported privately according to
[SECURITY.md](../SECURITY.md), not classified with a public `security` label.
After coordinated disclosure, classify public follow-up work by its deliverable.

## Priority

Every actionable open ticket has one priority value:

| Value | Meaning |
| --- | --- |
| `P0` | Active incident, severe security exposure, data loss, or a broadly unusable product. Interrupt normal work. |
| `P1` | Important correctness, security, or reliability work. Do next. |
| `P2` | Meaningful planned work. Schedule normally. |
| `P3` | Valid low-impact or opportunistic work. |

Priority ranks work the project intends to do. Leave it unset for rejected
work. Implementation size does not determine priority.

This repository stores priority in the Project `Priority` field. The
classification workflow sets it from evidence about impact, likelihood,
affected scope, and urgency. A human may change it.

## Status

Status owns the delivery lifecycle:

1. `Ready For Spec`
2. `Creating Spec`
3. `Ready To Implement`
4. `Implementing`
5. `Reviewing`
6. `Done`

Use the issue discussion and close reason for rejected, duplicate, invalid,
superseded, or missing-information outcomes. Record risk, dependencies,
verification requirements, and unresolved decisions in the ticket
specification rather than adding more labels or fields.

Moving a ticket to `Ready For Spec` or `Ready To Implement` authorizes the
corresponding Factory workflow. This repository stores status in the Project
`Status` field. The repository-owned Project source accepts issues based on
status regardless of author; Project write access is the authorization
boundary.

## Automated classification

The scheduled `classify-tickets` workflow reviews every open issue. It:

- reads `.factory/tickets.toml`;
- normalizes the issue to exactly one configured type value;
- sets exactly one configured priority value for actionable work;
- removes conflicting values from the same dimension;
- leaves status, issue content, and issue state unchanged.

The workflow treats issue content as untrusted evidence. It inspects the
repository and linked work before changing classification, and it makes no
change when the existing classification is correct.

## Storage backends

Use `storage = "project_field"` when the ticket system supports writable
single-select fields. Configure `project_owner`, `project_number`, and `field`
in that section.

Use `storage = "labels"` when it does not. In that mode, the values in the
section are the exact label names. The workflow replaces conflicting labels
within that dimension.

For label-backed status, configure the source to read issue state and labels,
and give each source trigger the corresponding configured status label. For
Project-backed status, configure a source that reads the same Project and field,
and give each trigger the corresponding status option. This repository's
`.factory/config.toml` is the Project-backed example.

The workflow must not invent fields, options, statuses, or labels. Missing
configured values are configuration errors: report them and make no partial
changes. A repository may use different storage backends for type, priority,
and status.
