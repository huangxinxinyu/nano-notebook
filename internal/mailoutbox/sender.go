package mailoutbox

import (
	"context"
	"errors"
	"fmt"
	"html"
	"strings"
	"time"
)

type Message struct {
	MessageID string
	To        string
	Subject   string
	TextBody  string
	HTMLBody  string
}

type Mailer interface {
	Send(context.Context, Message) error
}

type DeliveryQueue interface {
	Claim(context.Context) (Delivery, bool, error)
	Complete(context.Context, string, string) error
	Release(context.Context, string, string, string) error
}

type Sender struct {
	queue   DeliveryQueue
	mailer  Mailer
	baseURL string
}

func NewSender(queue DeliveryQueue, mailer Mailer, baseURL string) *Sender {
	return &Sender{queue: queue, mailer: mailer, baseURL: strings.TrimRight(baseURL, "/")}
}

func (s *Sender) Run(ctx context.Context, pollInterval time.Duration) error {
	if pollInterval <= 0 {
		return errors.New("mail poll interval must be positive")
	}
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-timer.C:
			attempted, err := s.SendOnce(ctx)
			delay := pollInterval
			if err == nil && attempted {
				delay = 0
			}
			timer.Reset(delay)
		}
	}
}

func (s *Sender) SendOnce(ctx context.Context) (bool, error) {
	if s == nil || s.queue == nil || s.mailer == nil || s.baseURL == "" {
		return false, errors.New("invalid mail Sender")
	}
	delivery, ok, err := s.queue.Claim(ctx)
	if err != nil || !ok {
		return false, err
	}
	message, err := s.render(delivery)
	if err != nil {
		_ = s.queue.Release(ctx, delivery.ID, delivery.LeaseToken, "render_failed")
		return true, err
	}
	if err := s.mailer.Send(ctx, message); err != nil {
		if releaseErr := s.queue.Release(ctx, delivery.ID, delivery.LeaseToken, "delivery_failed"); releaseErr != nil {
			return true, errors.Join(err, releaseErr)
		}
		return true, err
	}
	if err := s.queue.Complete(ctx, delivery.ID, delivery.LeaseToken); err != nil {
		return true, err
	}
	return true, nil
}

func (s *Sender) render(delivery Delivery) (Message, error) {
	if delivery.ID == "" || delivery.RecipientEmail == "" {
		return Message{}, errors.New("unsupported mail delivery")
	}
	if delivery.Kind == "notebook_deleted" && delivery.NotebookTitle != "" {
		subject := "A shared Nano Notebook was deleted"
		textBody := fmt.Sprintf("The shared Notebook %q was permanently deleted by its Owner.\n", delivery.NotebookTitle)
		if delivery.Locale == "zh-CN" {
			subject = "一个共享的 Nano Notebook 已被删除"
			textBody = fmt.Sprintf("共享笔记本“%s”已被所有者永久删除。\n", delivery.NotebookTitle)
		}
		return Message{MessageID: delivery.ID + "@nano-notebook.local", To: delivery.RecipientEmail,
			Subject: subject, TextBody: textBody, HTMLBody: `<p>` + html.EscapeString(textBody) + `</p>`}, nil
	}
	if delivery.Kind != "notebook_invitation" || delivery.Token == "" {
		return Message{}, errors.New("unsupported mail delivery")
	}
	link := s.baseURL + "/invitations/accept#token=" + delivery.Token
	subject := "You were invited to a Nano Notebook"
	textBody := fmt.Sprintf("Open this invitation to join the shared Notebook:\n%s\n", link)
	if delivery.Locale == "zh-CN" {
		subject = "你收到了 Nano Notebook 邀请"
		textBody = fmt.Sprintf("打开以下邀请加入共享 Notebook：\n%s\n", link)
	}
	return Message{
		MessageID: delivery.ID + "@nano-notebook.local",
		To:        delivery.RecipientEmail,
		Subject:   subject,
		TextBody:  textBody,
		HTMLBody:  `<p>` + html.EscapeString(textBody) + `</p>`,
	}, nil
}
