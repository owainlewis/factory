# Objective: Tighten Docs And Code Shape

## Goal

Make Factory easier to understand and safer to change by finding stale docs, inconsistent names, duplicated explanations, and small code clarity issues.

## Context

Factory is now dogfooding its own `.factory/` contract.
The current model is:

- target repos own `.factory/`
- Factory owns `.factory-state/`
- routine work uses the default `standards-check` workflow
- plan mode reports only
- execute mode may create a branch, edit files, commit, push, and open a draft PR
- Factory must not merge PRs

## Scope

- README
- docs
- `.factory/` dogfood files
- small behavior-preserving code simplifications only when they directly remove confusion

## Done

- stale or inconsistent docs are corrected
- unsupported claims are removed or tightened
- workflow names are consistent
- any code change is small, focused, and covered by existing checks
- one draft PR is opened for review

## Workflow

Use `.factory/WORKFLOWS/standards-check.md`.

## Stop Rules

- Do not redesign runner behavior.
- Do not add new features.
- Do not change config shape.
- Do not merge pull requests.
- Stop if the right fix requires product strategy.
