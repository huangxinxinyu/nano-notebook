# Memory Policy

## Canonical Path

Each delivered requirement gets one run memory file:

```text
memory/runs/<parent-identifier>.md
```

Use the parent Issue identifier form, for example `memory/runs/NAN-1.md`.

## Writer

Codex is the only writer. Delivery Expert, QA, and Reviewer do not write or edit repository memory files.

## Commit Contract

Write run memory only after QA and Review pass the same `final_sha` and Codex has fast-forwarded and pushed the target branch to that exact SHA. Commit only `memory/runs/<parent-identifier>.md`, push normally, and record the resulting commit as `memory_commit_sha`.

The target branch must equal `final_sha` before this commit and contain `final_sha` as an ancestor afterward. The memory-only commit does not require another QA and Review wave. Codex must verify the path-only diff, required content, remote tip, and ancestry. Any additional changed path or failed commit, push, or verification blocks parent completion.

## Required Content

Each run memory file should capture only:

- approved requirement summary
- approved plan summary
- key decisions and constraints
- `final_sha`
- accepted QA and Review evidence references
- outcome and reusable lessons

## Exclusions

Do not copy:

- full Multica comments
- run transcripts
- raw test logs
- transient status chatter

## Promotion Rules

`memory/knowledge/` is for reviewed, stable knowledge only.

`memory/improvements/` is for proposed changes to the framework, skills, or agent instructions.

Process output does not promote itself automatically into long-term knowledge. Promotion requires review during a later change.
