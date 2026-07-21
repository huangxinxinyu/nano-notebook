package mailoutbox

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestSenderDeliversInvitationBeforeCompletingLease(t *testing.T) {
	queue := &recordingDeliveryQueue{delivery: Delivery{
		ID: "mail_123", Kind: "notebook_invitation", InvitationID: "inv_123",
		RecipientEmail: "viewer@example.com", Locale: "en", Token: "secret-token", LeaseToken: "lease-123",
	}, available: true}
	mailer := &recordingMailer{}
	sender := NewSender(queue, mailer, "http://localhost:5173")

	attempted, err := sender.SendOnce(context.Background())
	if err != nil || !attempted {
		t.Fatalf("SendOnce attempted=%t err=%v", attempted, err)
	}
	if len(mailer.messages) != 1 {
		t.Fatalf("messages=%d", len(mailer.messages))
	}
	message := mailer.messages[0]
	if message.MessageID != "mail_123@nano-notebook.local" || message.To != "viewer@example.com" ||
		!strings.Contains(message.TextBody, "http://localhost:5173/invitations/accept#token=secret-token") {
		t.Fatalf("message=%+v", message)
	}
	if queue.completedID != "mail_123" || queue.completedLease != "lease-123" {
		t.Fatalf("completed id=%q lease=%q", queue.completedID, queue.completedLease)
	}
}

func TestSenderRunStopsOnCancellation(t *testing.T) {
	queue := &recordingDeliveryQueue{}
	sender := NewSender(queue, &recordingMailer{}, "http://localhost:5173")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- sender.Run(ctx, time.Millisecond) }()
	time.Sleep(5 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Sender did not stop after cancellation")
	}
}

func TestSenderDeliversNotebookDeletionNotification(t *testing.T) {
	queue := &recordingDeliveryQueue{delivery: Delivery{
		ID: "mail_deleted", Kind: "notebook_deleted", RecipientEmail: "former@example.com",
		Locale: "zh-CN", NotebookTitle: "共享研究", LeaseToken: "lease-deleted",
	}, available: true}
	mailer := &recordingMailer{}
	attempted, err := NewSender(queue, mailer, "http://localhost:5173").SendOnce(context.Background())
	if err != nil || !attempted || len(mailer.messages) != 1 {
		t.Fatalf("attempted=%t err=%v messages=%d", attempted, err, len(mailer.messages))
	}
	if !strings.Contains(mailer.messages[0].TextBody, "共享研究") || queue.completedID != "mail_deleted" {
		t.Fatalf("message=%+v completed=%q", mailer.messages[0], queue.completedID)
	}
}

type recordingDeliveryQueue struct {
	delivery       Delivery
	available      bool
	completedID    string
	completedLease string
}

func (q *recordingDeliveryQueue) Claim(context.Context) (Delivery, bool, error) {
	return q.delivery, q.available, nil
}

func (q *recordingDeliveryQueue) Complete(_ context.Context, id, lease string) error {
	q.completedID = id
	q.completedLease = lease
	return nil
}

func (q *recordingDeliveryQueue) Release(context.Context, string, string, string) error {
	return nil
}

type recordingMailer struct {
	messages []Message
}

func (m *recordingMailer) Send(_ context.Context, message Message) error {
	m.messages = append(m.messages, message)
	return nil
}
