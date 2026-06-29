# What makes a great software project

If you use coding agents for long enough, you notice a strange thing.
They can write code quickly, but they still need a project to tell them what good means.

The agent can add a feature.
It can fix a bug.
It can update a README.
But if the repository has no clear standards, no build path, no release process, and no review rules, the agent has to guess.

That is where many projects fall apart.
Not because nobody can write the code.
Because nobody keeps the whole project healthy.

The missing artifact is a simple checklist for professional software.

## The simple version

A great software project is not just a folder of working code.

It is a project that a new person can understand, build, test, use, review, and release without needing secret knowledge from the original author.

That means the repo needs more than source files.
It needs a visible engineering system.

At minimum, a serious project answers these questions:

- What is this?
- Who is it for?
- How do I install it?
- How do I build it?
- How do I test it?
- How do I run it?
- How do I release it?
- How do I know a change is safe?
- How do I report or pick up work?
- What must a human review?

Those questions apply to almost every software project.
The answers change by language and project type.

## What is actually happening

When a senior engineer reviews a repository, they do not only read the code.
They inspect the project system around the code.

They look for things like:

```text
README.md
LICENSE
CHANGELOG.md
.github/workflows/
docs/
tests/
package metadata
release process
issue labels
contribution rules
```

Then they ask whether a stranger can move through the project:

```text
clone
install
build
test
run
change
review
release
debug
```

If any step only exists in one person's head, the project is weaker than it looks.

This is why small bits of project hygiene matter so much.
An empty GitHub description is not a disaster.
A missing CI file is not always urgent.
A stale README is not a broken product.

But together, these gaps make a project hard to trust and hard to improve.

## Why people get this wrong

Developers often treat this work as admin.

They think the real work is the code, and the rest can wait.
That is understandable.
Writing CI, release notes, docs, labels, and setup instructions can feel like friction.

But this work is not separate from engineering.
It is what lets engineering compound.

Without it, every future change becomes more expensive.

The next person has to rediscover how tests run.
The next release is manual.
The next bug fix ships without regression coverage.
The next agent run starts from scratch.

The project still moves, but it moves by memory.
Memory does not scale.

## The hard part

The hard part is that "good software project" is both generic and specific.

The buckets are generic:

- identity
- usability
- build
- testing
- CI
- code quality
- documentation
- release
- security
- operations
- governance
- agent readiness

But the answers are specific.

For a Go CLI, testing might mean:

```sh
go test ./...
go vet ./...
go build ./...
```

For an OCaml interpreter, testing might mean:

```sh
opam install . --deps-only --with-test
dune build
dune runtest
```

For a web app, the important checks may include typecheck, lint, unit tests, browser tests, accessibility checks, and deployment previews.

The mistake is trying to make one flat checklist for every project.
The better model is:

```text
same buckets
different language rules
different project type rules
repo-owned final standard
```

That gives you consistency without pretending every repo is the same.

## A practical example

Take a small interpreter project written in OCaml.

The code may be good.
It may parse expressions, evaluate programs, run examples, and pass tests.

But a new user still needs to know:

- what Scheme standard it targets
- how complete the implementation is
- how to install OCaml and Dune
- how to build the interpreter
- how to run one expression
- how to run a file
- how to run tests
- whether releases exist
- whether prebuilt binaries exist
- how to report missing language features

An agent needs even more structure.

It needs to know:

- which standard to implement against
- where conformance is tracked
- what tests to add for a new language feature
- what branch name to use
- what commands prove the change
- when to stop for human review

So the repo should not only have code.
It should have files like:

```text
AGENTS.md
STANDARDS.md
WORKFLOWS/
  standards-check.md
  ci-readiness.md
  release-readiness.md
OBJECTIVES/
  2026-06-29-release-readiness.md
JOURNAL.md
```

The standards say what good looks like.
The workflows say how an agent should improve one part.
The objectives say what outcome is wanted now.
The journal gives future runs context.

Now the agent is not being asked to guess what a professional project needs.
It is being asked to execute a known engineering workflow.

## Where AI coding agents change the problem

AI coding agents make this checklist more important, not less.

Before agents, missing project process slowed humans down.
With agents, missing project process causes automation to drift.

An agent can work very quickly in the wrong direction.
It can change code without knowing the release rules.
It can update docs without knowing the project claims.
It can fix an issue without knowing what checks matter.
It can open a pull request that looks useful but cannot be merged safely.

Agents need boundaries.
They need:

- standards
- workflows
- stop rules
- verification commands
- human review rules
- evidence requirements

This is not about making agents less capable.
It is about giving them the same project context a good human engineer would use.

## What belongs in the checklist

A useful project standard should cover these buckets.

### Identity

The project should clearly say what it is, who it is for, and what state it is in.

This includes the GitHub description, README purpose, project status, and public claims.

### Usability

A new person should be able to install, build, test, and run the project from a clean checkout.

The README should contain real commands.
At least one example should work.

### Correctness

The project should have tests for important behavior.
Bug fixes should add regression tests.
Examples should either be tested or clearly marked as examples.

### CI and automation

Pull requests should run build and test checks.
CI should fail on real failures.
Normal checks should not require secrets.

This bucket also includes useful automation like formatting, linting, typechecking, dependency checks, and generated file checks.

### Code quality

Code should be readable, idiomatic, and maintainable.
AI code review can help here, but it should be treated as a first pass.

Human review is still required before merge.
Security-sensitive changes need human review.

### Documentation

Docs should match the current code.
README, examples, architecture notes, and API docs should not tell different stories.

### Release

The project should explain how it ships.

For a CLI, that may mean tagged releases and downloadable binaries.
For a library, it may mean package metadata and publish steps.
For a service, it may mean deploy and rollback docs.

### Security

Secrets should not be committed.
Dependencies should have an update path.
CI permissions should be minimal.

### Operations

If the project runs as a service, it should explain environment variables, health checks, logs, deployment, and rollback.

Not every project needs this bucket in full.
But every runnable system needs some operational story.

### Governance

License, contribution rules, issue labels, ownership, and human review rules should be clear.

This is also where agent labels belong:

```text
factory-ready
factory-triage
factory-needs-human
factory-blocked
```

### Agent readiness

The repo should be safe for agents to work on.

That means clear instructions, workflows, standards, verification commands, and stop rules.

## Where this breaks

The checklist can become busywork if it is treated as a law.

Not every repo needs every section at the same depth.
A private experiment does not need a full release process.
A docs-only repo does not need a compiler.
A prototype may only need a README, license, and a basic test command.

The standard should scale by project type.

The useful question is not:

```text
Does every repo have every possible artifact?
```

The useful question is:

```text
Can this repo be understood, changed, verified, and shipped at the level it claims?
```

## How to start

Start with one repo.

Create a `STANDARDS.md` file with the common buckets.
Fill in the language-specific commands.
Then add one workflow:

```text
WORKFLOWS/standards-check.md
```

The workflow should do one thing:

```text
Compare the repo against STANDARDS.md.
In plan mode, report gaps.
In execute mode, fix one small gap and open a draft pull request.
```

When you want a directed piece of work, add one objective:

```text
OBJECTIVES/2026-06-29-release-readiness.md
```

That objective should say the goal, scope, done conditions, workflow, and stop rules.

That is enough to make the repo better.

The larger idea is simple:

```text
Factory gives every project a senior engineer memory.
Agents use that memory to improve the project safely.
```

That may be the most useful part of the system.
Even before the runner is powerful, the standard tells you what you forgot to do.
