package mailoutbox

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"
)

func TestSMTPMailerSendsBoundedRFCMessage(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	received := make(chan string, 1)
	go serveOneSMTPMessage(listener, received)

	mailer := NewSMTPMailer(listener.Addr().String(), "nano@localhost", 2*time.Second)
	err = mailer.Send(context.Background(), Message{
		MessageID: "mail_123@nano-notebook.local", To: "viewer@example.com",
		Subject: "Notebook invitation", TextBody: "Open http://localhost/invitations/accept#token=abc",
		HTMLBody: "<p>Open invitation</p>",
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case message := <-received:
		for _, expected := range []string{
			"Message-ID: <mail_123@nano-notebook.local>",
			"To: viewer@example.com",
			"Subject: Notebook invitation",
			"token=abc",
		} {
			if !strings.Contains(message, expected) {
				t.Fatalf("SMTP message omitted %q:\n%s", expected, message)
			}
		}
	case <-time.After(2 * time.Second):
		t.Fatal("SMTP server did not receive a message")
	}
}

func serveOneSMTPMessage(listener net.Listener, received chan<- string) {
	connection, err := listener.Accept()
	if err != nil {
		return
	}
	defer connection.Close()
	reader := bufio.NewReader(connection)
	writer := bufio.NewWriter(connection)
	writeSMTPLine(writer, "220 localhost ESMTP")
	inData := false
	var body strings.Builder
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return
		}
		trimmed := strings.TrimRight(line, "\r\n")
		if inData {
			if trimmed == "." {
				received <- body.String()
				writeSMTPLine(writer, "250 queued")
				inData = false
				continue
			}
			body.WriteString(line)
			continue
		}
		upper := strings.ToUpper(trimmed)
		switch {
		case strings.HasPrefix(upper, "EHLO"), strings.HasPrefix(upper, "HELO"):
			writeSMTPLine(writer, "250-localhost")
			writeSMTPLine(writer, "250 8BITMIME")
		case strings.HasPrefix(upper, "MAIL FROM:"), strings.HasPrefix(upper, "RCPT TO:"):
			writeSMTPLine(writer, "250 ok")
		case upper == "DATA":
			writeSMTPLine(writer, "354 end with dot")
			inData = true
		case upper == "QUIT":
			writeSMTPLine(writer, "221 bye")
			return
		default:
			writeSMTPLine(writer, "500 unsupported")
		}
	}
}

func writeSMTPLine(writer *bufio.Writer, line string) {
	_, _ = fmt.Fprintf(writer, "%s\r\n", line)
	_ = writer.Flush()
}
