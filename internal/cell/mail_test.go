package cell

import (
	"bufio"
	"context"
	"net"
	"strings"
	"testing"
	"time"
)

// TestMailGreeting: the client greets with the sender's domain.
func TestMailGreeting(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	got := make(chan string, 1)
	go func() {
		conn, err := l.Accept()
		if err != nil {
			got <- err.Error()
			return
		}
		defer func() { _ = conn.Close() }()
		_, _ = conn.Write([]byte("220 test\r\n"))
		line, _ := bufio.NewReader(conn).ReadString('\n')
		got <- strings.TrimSpace(line)
		_, _ = conn.Write([]byte("250 test\r\n"))
	}()
	port := l.Addr().(*net.TCPAddr).Port
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	m := Mail{Host: "127.0.0.1", Port: port, From: "cell@cell.example.com"}
	if _, err := m.Dial(ctx); err == nil || !strings.Contains(err.Error(), "STARTTLS") {
		t.Errorf("Dial without STARTTLS: %v, want its error", err)
	}
	if line := <-got; line != "EHLO cell.example.com" {
		t.Errorf("greeting %q, want EHLO cell.example.com", line)
	}
}
