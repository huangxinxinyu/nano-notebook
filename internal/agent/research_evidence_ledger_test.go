package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// recordingLedgerTx records ledger writes; every other pgx.Tx method is unused.
type recordingLedgerTx struct {
	pgx.Tx
	statements []string
	args       [][]any
}

func (tx *recordingLedgerTx) Exec(_ context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	tx.statements = append(tx.statements, sql)
	tx.args = append(tx.args, args)
	return pgconn.NewCommandTag("INSERT 0 1"), nil
}

func TestExternalizedReadURLResultIsRecordedAsRead(t *testing.T) {
	sha := strings.Repeat("ab", 32)
	projection, _ := json.Marshal(ToolResultProjection{ContentState: ToolResultExternalized, Preview: `{"title":"FAIR-RAG"`, ResultRef: "tr_abcdefghijklmnop", ResultBytes: 157091, SHA256: sha})
	input, _ := json.Marshal(readURLInput{URL: "https://arxiv.org/html/2510.22344v1"})
	tx := &recordingLedgerTx{}
	action := AcceptedAction{ActionID: "decision:6/action:0", Name: "read_url", Input: input, Result: &ActionResult{Status: ActionSucceeded, Output: projection}}
	if err := materializeResearchEvidence(context.Background(), tx, "research_s", "run_r", action); err != nil {
		t.Fatal(err)
	}
	if len(tx.statements) != 1 || !strings.Contains(tx.statements[0], "'read'") {
		t.Fatalf("statements=%q", tx.statements)
	}
	if got := tx.args[0]; got[1] != "https://arxiv.org/html/2510.22344v1" || got[3] != "decision:6/action:0" || got[4] != sha {
		t.Fatalf("args=%v", got)
	}
}

func TestDelegatedReadStaysALeadInTheLedger(t *testing.T) {
	output, _ := json.Marshal(readURLOutput{Outcome: researchReaderOutcome, RequestedURL: "https://example.com/paper", Title: "Paper", FinalURL: "https://example.com/paper", ReaderAgentID: "run_reader"})
	input, _ := json.Marshal(readURLInput{URL: "https://example.com/paper"})
	tx := &recordingLedgerTx{}
	action := AcceptedAction{ActionID: "decision:3/action:1", Name: "read_url", Input: input, Result: &ActionResult{Status: ActionSucceeded, Output: output}}
	if err := materializeResearchEvidence(context.Background(), tx, "research_s", "run_r", action); err != nil {
		t.Fatal(err)
	}
	if len(tx.statements) != 1 || !strings.Contains(tx.statements[0], "'discovered'") || strings.Contains(tx.statements[0], "status='read'") {
		t.Fatalf("statements=%q", tx.statements)
	}
}
