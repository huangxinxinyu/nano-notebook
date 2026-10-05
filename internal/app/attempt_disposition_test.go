package app

import (
	"strings"
	"testing"
)

func TestAttemptDispositionMigrationAddsActiveRetryScheduling(t *testing.T) {
	for _, required := range []string{
		"available_at timestamptz not null default now()",
		"last_error_code text",
		"agent_jobs(available_at,created_at,id)",
		"attempt_no between 0 and 100",
		"failed_attempts between 0 and 10",
	} {
		if !strings.Contains(migrationsSQL, required) {
			t.Fatalf("Attempt disposition migration is missing %q", required)
		}
	}
}
