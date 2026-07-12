# Memory Policy

## Canonical Path

Each delivered requirement gets one run memory file:

```text
memory/runs/<parent-identifier>.md
```

Use the parent Issue identifier form, for example `memory/runs/NAN-1.md`.

## Writer

Codex is the only writer. Delivery Expert, QA, and Reviewer do not write or edit repository memory files.

## Required Content

Each run memory file should capture only:

- approved requirement summary
- approved plan summary
- key decisions and constraints
- final accepted SHA
- QA and Review evidence references
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
