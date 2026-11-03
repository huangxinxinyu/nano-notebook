package agent

import (
	"context"
	"fmt"
	"strings"
)

// SteeringMessage is a Member message sent to a Run that is still working.
// Delivery means the Controller checkpointed it before a decision; until then
// it is pending.
type SteeringMessage struct {
	ID      string
	Content string
}

// SteeringRuntime lists the Steering Messages a Run has not delivered yet, in
// the order the Member sent them. The Controller delivers them at the next
// decision boundary: after the current Action batch completes and before the
// model is asked for another decision, matching Pi's steering queue.
type SteeringRuntime interface {
	PendingSteering(ctx context.Context, attempt Attempt) ([]SteeringMessage, error)
}

// PendingSteering returns the Run's undelivered Steering Messages. Delivery is
// derived from the Run's own checkpoints, so a crashed Attempt never delivers
// a message twice and never loses one.
func (r *PostgresRuntime) PendingSteering(ctx context.Context, attempt Attempt) ([]SteeringMessage, error) {
	tx, err := r.workerTx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `
		select m.id,m.content
		from chat_messages m
		where m.steering_run_id=$1 and m.role='user'
		  and not exists (
			select 1 from agent_run_checkpoints c
			where c.run_id=$1 and c.kind='steering_message' and c.payload->>'message_id'=m.id
		  )
		order by m.created_at,m.id
	`, attempt.RunID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	pending := make([]SteeringMessage, 0)
	for rows.Next() {
		var message SteeringMessage
		if err := rows.Scan(&message.ID, &message.Content); err != nil {
			return nil, err
		}
		pending = append(pending, message)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return pending, nil
}

// PendingSteering forwards to the base runtime. Only the Research root can be
// steered; a subagent's Run never owns Steering Messages.
func (r *ResearchRuntime) PendingSteering(ctx context.Context, attempt Attempt) ([]SteeringMessage, error) {
	return r.base.PendingSteering(ctx, attempt)
}

// researchSteeringRecitation renders every delivered Steering Message as one
// trailing User message. Research keeps steering out of the trajectory because
// archival capsules and task memories summarize Agent Steps by decision range
// and would drop a one-off user message; reciting at the tail keeps the
// guidance in force for the rest of the Run and leaves the cached prefix
// untouched.
func researchSteeringRecitation(steering []AcceptedSteering) string {
	if len(steering) == 0 {
		return ""
	}
	var builder strings.Builder
	builder.WriteString("Steering from the Member, sent while this Research was running (earliest first). Each entry names the decision it arrived before. Follow the latest entry where entries conflict; it adjusts the Accepted Research Plan without replacing it.\n")
	for index, entry := range steering {
		fmt.Fprintf(&builder, "\n%d. (before decision %d) %s", index+1, entry.DecisionNo, strings.TrimSpace(entry.Content))
	}
	return builder.String()
}
