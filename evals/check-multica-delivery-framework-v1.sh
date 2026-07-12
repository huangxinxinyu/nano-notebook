#!/usr/bin/env bash

set -euo pipefail

check_contains() {
  local file=$1
  local pattern=$2

  if ! rg -F -q -- "$pattern" "$file"; then
    echo "Missing required contract text in $file: $pattern" >&2
    exit 1
  fi
}

check_absent() {
  local file=$1
  local pattern=$2

  if rg -F -q -- "$pattern" "$file"; then
    echo "Found contradictory contract text in $file: $pattern" >&2
    exit 1
  fi
}

check_contains "skills/multica-delivery/SKILL.md" "Delivery Expert pushes the candidate branch after required checks pass."
check_contains "skills/multica-delivery/SKILL.md" "Only Codex may fast-forward merge the accepted candidate into the target branch."
check_contains "skills/multica-delivery/references/workflow.md" "Codex verifies the remote candidate branch and SHA before opening QA and Review."
check_contains "skills/multica-delivery/references/workflow.md" "Never force push the candidate branch."
check_contains "skills/multica-delivery/references/gates.md" "Do not open verification against a candidate SHA that is not reachable on the named remote branch."
check_contains "skills/multica-delivery/references/gates.md" "If the target branch head moved after candidate creation, block acceptance and create an integration or rework path from the new target head."
check_contains "skills/multica-delivery/references/recovery.md" "- \`candidate_branch\`"
check_contains "skills/multica-delivery/references/recovery.md" "- \`target_branch\`"
check_contains "skills/multica-delivery/references/recovery.md" "- \`target_head_sha\`"
check_contains "docs/superpowers/multica-delivery-framework.md" "Codex verifies the remote branch tip matches the reported candidate SHA before dispatching QA and Review."
check_contains "docs/superpowers/multica-delivery-framework.md" "If the target branch head changes after candidate creation, stop acceptance, create an integration or rework path from the new target head, and rerun verification on the replacement SHA."
check_contains "docs/superpowers/specs/2026-07-12-multica-delivery-framework-design.md" "Codex-owned fast-forward-only acceptance merge and target-branch push are in scope after exact-SHA QA and Review pass."
check_contains "docs/superpowers/specs/2026-07-12-multica-delivery-framework-design.md" "goal_identifier"
check_contains "docs/superpowers/specs/2026-07-12-multica-delivery-framework-design.md" "queue_state"
check_contains "docs/superpowers/specs/2026-07-12-multica-delivery-framework-design.md" "queue_position"
check_contains "docs/superpowers/specs/2026-07-12-multica-delivery-framework-design.md" "memory/runs/<parent-identifier>.md"
check_absent "docs/superpowers/specs/2026-07-12-multica-delivery-framework-design.md" "Release, merge, deployment, tagging, or production observation."
check_absent "docs/superpowers/specs/2026-07-12-multica-delivery-framework-design.md" "memory/runs/<issue-id>.md"
check_contains "agents/delivery-expert.md" "REMOTE_BRANCH: <remote branch or N/A>"
check_contains "templates/implementation-issue.md" "Push HEAD without force to the named candidate branch."
check_contains "templates/implementation-issue.md" "REMOTE_BRANCH: <remote branch or N/A>"
check_contains "templates/qa-issue.md" "- Candidate branch: \`<candidate-branch>\`"
check_contains "templates/review-issue.md" "- Candidate branch: \`<candidate-branch>\`"
check_contains "templates/parent-issue.md" "- \`candidate_branch\`: \`<remote-candidate-branch>\`"
check_contains "templates/parent-issue.md" "- \`target_branch\`: \`<target-branch>\`"
check_contains "templates/parent-issue.md" "- \`target_head_sha\`: \`<sha-recorded-at-candidate-creation>\`"
check_contains "config/multica.example.toml" "candidate_branch = \"multica/<parent-identifier>\""
check_contains "config/multica.example.toml" "target_branch = \"main\""
check_absent "config/multica.example.toml" "verification_branch = "
check_contains "evals/multica-delivery-framework-v1.md" "## 7. Candidate Push Success"
check_contains "evals/multica-delivery-framework-v1.md" "## 8. Push Failure or Unreachable Remote"
check_contains "evals/multica-delivery-framework-v1.md" "## 9. Remote SHA Verification Before QA and Review"
check_contains "evals/multica-delivery-framework-v1.md" "## 10. Fast-Forward Acceptance Merge"
check_contains "evals/multica-delivery-framework-v1.md" "## 11. Target Branch Drift Blocks Merge"
check_contains "evals/multica-delivery-framework-v1.md" "## 12. Merge or Push Failure Stays Blocked"
check_contains "evals/multica-delivery-framework-v1.md" "## 13. Force Push and Self-Merge Are Prohibited"

echo "multica-delivery-v1 contract checks passed"
