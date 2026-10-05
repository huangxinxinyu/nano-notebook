package agent

import (
	"context"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"
)

// One document gets one reader. An arXiv paper reaches the Run under several
// URLs (abs, html, pdf, ar5iv), so readers are keyed by arXiv id, and other
// documents by their normalized URL.

var (
	researchArxivDocumentPattern = regexp.MustCompile(`(?i)(?:^|//)(?:www\.)?(?:arxiv\.org|ar5iv\.labs\.arxiv\.org|ar5iv\.org)/(?:abs|pdf|html)/(\d{4}\.\d{4,5})`)
	researchReaderTaskURLPattern = regexp.MustCompile(`^\s*(https?://\S+)`)
)

func researchDocumentKey(url string) string {
	if match := researchArxivDocumentPattern.FindStringSubmatch(url); match != nil {
		return "arxiv:" + match[1]
	}
	return normalizeClaimURL(url)
}

// researchReaderTaskURL returns the document a reader task names.
func researchReaderTaskURL(message string) string {
	if !isResearchReaderTask(message) {
		return ""
	}
	// The task reads "<prefix> <url> (<title>)."
	if match := researchReaderTaskURLPattern.FindStringSubmatch(strings.TrimPrefix(message, researchReaderTaskPrefix)); match != nil {
		return match[1]
	}
	return ""
}

// researchReaderDocumentKeysInTx lists the documents the parent's readers
// cover.
func researchReaderDocumentKeysInTx(ctx context.Context, tx DBTX, parentID string) (map[string]bool, error) {
	rows, err := tx.Query(ctx, `select message from agent_subagents where parent_run_id=$1 and starts_with(message,$2)`, parentID, researchReaderTaskPrefix)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	keys := map[string]bool{}
	for rows.Next() {
		var message string
		if err := rows.Scan(&message); err != nil {
			return nil, err
		}
		if url := researchReaderTaskURL(message); url != "" {
			keys[researchDocumentKey(url)] = true
		}
	}
	return keys, rows.Err()
}

// findResearchReaderForDocumentInTx returns the parent's existing reader for
// the same document, if any.
func findResearchReaderForDocumentInTx(ctx context.Context, tx pgx.Tx, parentID, url string) (string, string, bool, error) {
	key := researchDocumentKey(url)
	if key == "" {
		return "", "", false, nil
	}
	rows, err := tx.Query(ctx, `select child_run_id,task_name,message from agent_subagents
		where parent_run_id=$1 and starts_with(message,$2) order by created_at,child_run_id`, parentID, researchReaderTaskPrefix)
	if err != nil {
		return "", "", false, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, name, message string
		if err := rows.Scan(&id, &name, &message); err != nil {
			return "", "", false, err
		}
		if existing := researchReaderTaskURL(message); existing != "" && researchDocumentKey(existing) == key {
			return id, name, true, nil
		}
	}
	return "", "", false, rows.Err()
}
