package mailoutbox

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/smtp"
	"strings"
	"time"
)

const maxMailBodyBytes = 256 * 1024

type SMTPMailer struct {
	address string
	from    string
	timeout time.Duration
}

func NewSMTPMailer(address, from string, timeout time.Duration) *SMTPMailer {
	return &SMTPMailer{address: strings.TrimSpace(address), from: strings.TrimSpace(from), timeout: timeout}
}

func (m *SMTPMailer) Send(ctx context.Context, message Message) error {
	if m == nil || m.address == "" || m.from == "" || m.timeout <= 0 ||
		invalidHeader(m.from) || invalidHeader(message.MessageID) || invalidHeader(message.To) || invalidHeader(message.Subject) ||
		message.MessageID == "" || message.To == "" || message.Subject == "" {
		return errors.New("invalid SMTP message")
	}
	payload, err := encodeMessage(m.from, message)
	if err != nil {
		return err
	}
	host, _, err := net.SplitHostPort(m.address)
	if err != nil {
		return err
	}
	dialer := net.Dialer{Timeout: m.timeout}
	connection, err := dialer.DialContext(ctx, "tcp", m.address)
	if err != nil {
		return err
	}
	defer connection.Close()
	deadline := time.Now().Add(m.timeout)
	if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
		deadline = contextDeadline
	}
	if err := connection.SetDeadline(deadline); err != nil {
		return err
	}
	client, err := smtp.NewClient(connection, host)
	if err != nil {
		return err
	}
	defer client.Close()
	if err := client.Mail(m.from); err != nil {
		return err
	}
	if err := client.Rcpt(message.To); err != nil {
		return err
	}
	writer, err := client.Data()
	if err != nil {
		return err
	}
	if _, err := writer.Write(payload); err != nil {
		_ = writer.Close()
		return err
	}
	if err := writer.Close(); err != nil {
		return err
	}
	return client.Quit()
}

func encodeMessage(from string, message Message) ([]byte, error) {
	if len(message.TextBody)+len(message.HTMLBody) > maxMailBodyBytes {
		return nil, errors.New("mail body exceeds limit")
	}
	const boundary = "nano-notebook-mail-boundary"
	var body bytes.Buffer
	fmt.Fprintf(&body, "From: %s\r\n", from)
	fmt.Fprintf(&body, "To: %s\r\n", message.To)
	fmt.Fprintf(&body, "Message-ID: <%s>\r\n", message.MessageID)
	fmt.Fprintf(&body, "Subject: %s\r\n", mime.QEncoding.Encode("UTF-8", message.Subject))
	body.WriteString("MIME-Version: 1.0\r\n")
	fmt.Fprintf(&body, "Content-Type: multipart/alternative; boundary=%q\r\n\r\n", boundary)
	fmt.Fprintf(&body, "--%s\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\n%s\r\n", boundary, normalizeCRLF(message.TextBody))
	fmt.Fprintf(&body, "--%s\r\nContent-Type: text/html; charset=UTF-8\r\n\r\n%s\r\n", boundary, normalizeCRLF(message.HTMLBody))
	fmt.Fprintf(&body, "--%s--\r\n", boundary)
	return body.Bytes(), nil
}

func invalidHeader(value string) bool {
	return strings.ContainsAny(value, "\r\n")
}

func normalizeCRLF(value string) string {
	value = strings.ReplaceAll(value, "\r\n", "\n")
	value = strings.ReplaceAll(value, "\r", "\n")
	return strings.ReplaceAll(value, "\n", "\r\n")
}
