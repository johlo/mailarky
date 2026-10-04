package sandbox

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"net/textproto"
	"os"
	"strings"
	"testing"
	"time"
)

// These tests use real sockets and independent protocol readers. The server
// hooks and backend methods are never called by the client side of a test.
func faultWire(t *testing.T, address string, secure bool) *textproto.Conn {
	t.Helper()
	var c net.Conn
	var err error
	if secure {
		pem, e := os.ReadFile("../../testdata/tls/server.crt")
		if e != nil {
			t.Fatal(e)
		}
		roots := x509.NewCertPool()
		roots.AppendCertsFromPEM(pem)
		c, err = tls.DialWithDialer(&net.Dialer{Timeout: 3 * time.Second}, "tcp", address, &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12})
	} else {
		c, err = net.DialTimeout("tcp", address, 3*time.Second)
	}
	if err != nil {
		t.Fatal(err)
	}
	c.SetDeadline(time.Now().Add(5 * time.Second))
	w := textproto.NewConn(c)
	t.Cleanup(func() { w.Close() })
	return w
}
func smtpReply(t *testing.T, w *textproto.Conn, command string, code int) string {
	t.Helper()
	if command != "" {
		if err := w.PrintfLine("%s", command); err != nil {
			t.Fatal(err)
		}
	}
	_, msg, err := w.ReadResponse(code)
	if err != nil {
		t.Fatalf("%s: %v", command, err)
	}
	return msg
}
func smtpWire(t *testing.T, address string) *textproto.Conn {
	t.Helper()
	w := faultWire(t, address, false)
	smtpReply(t, w, "", 220)
	smtpReply(t, w, "EHLO localhost", 250)
	return w
}
func imapReply(t *testing.T, w *textproto.Conn, command, status string) string {
	t.Helper()
	if err := w.PrintfLine("a %s", command); err != nil {
		t.Fatal(err)
	}
	var result []string
	for {
		line, err := w.ReadLine()
		if err != nil {
			t.Fatalf("%s: %v (%v)", command, err, result)
		}
		result = append(result, line)
		if strings.HasPrefix(line, "a ") {
			if !strings.HasPrefix(line, "a "+status+" ") {
				t.Fatalf("%s: %s", command, line)
			}
			return strings.Join(result, "\n")
		}
	}
}
func imapWire(t *testing.T, address, username, password string) *textproto.Conn {
	t.Helper()
	w := faultWire(t, address, true)
	if line, err := w.ReadLine(); err != nil || !strings.HasPrefix(line, "* OK") {
		t.Fatalf("greeting: %q %v", line, err)
	}
	if username != "" {
		imapReply(t, w, fmt.Sprintf("LOGIN %q %q", username, password), "OK")
	}
	return w
}
func assertClosed(t *testing.T, w *textproto.Conn) {
	t.Helper()
	if line, err := w.ReadLine(); err == nil {
		t.Fatalf("connection still open: %q", line)
	} else if e, ok := err.(net.Error); ok && e.Timeout() {
		t.Fatal("connection timed out instead of closing")
	}
}
