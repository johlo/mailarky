package sandbox

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"github.com/johlo/go-imap/v2"
	client "github.com/johlo/go-imap/v2/imapclient"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

const defaultAPI = "/api/v1/accounts/default"

func testManager(t *testing.T, c configuration) (*Service, http.Handler) {
	t.Helper()
	service, err := openService(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { service.Close() })
	return service, newAPI(service).handler()
}
func testStore(t *testing.T, c configuration) *Account {
	t.Helper()
	service, _ := testManager(t, c)
	return service.lookup(defaultAccountID)
}
func controlHandler(a *Account) http.Handler { return newAPI(a.service).handler() }
func smtpAddress(t *testing.T, a *Account) string {
	t.Helper()
	server, err := newSMTPServer(a.service)
	if err != nil {
		t.Fatal(err)
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- server.Serve(l) }()
	t.Cleanup(func() { server.Close(); l.Close(); <-done })
	return l.Addr().String()
}
func imapAddress(t *testing.T, service *Service) string {
	t.Helper()
	cert, err := tls.LoadX509KeyPair("../../testdata/tls/server.crt", "../../testdata/tls/server.key")
	if err != nil {
		t.Fatal(err)
	}
	s := newIMAPServer(service)
	l, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- s.Serve(l) }()
	t.Cleanup(func() { s.Close(); l.Close(); <-done })
	return l.Addr().String()
}
func createAccount(t *testing.T, h http.Handler, name string) mailboxDescription {
	t.Helper()
	w := apiCall(t, h, "POST", "/api/v1/accounts", mailboxCreation{Name: name, Username: name, Password: "password-" + name, Recipients: []string{name + "@example.test"}}, 201)
	var a mailboxDescription
	if err := json.Unmarshal(w.Body.Bytes(), &a); err != nil {
		t.Fatal(err)
	}
	return a
}
func accountCount(t *testing.T, h http.Handler, base string) int {
	t.Helper()
	var list messagesSummary
	if err := json.Unmarshal(apiCall(t, h, "GET", base+"/messages", nil, 200).Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	return list.Total
}
func imapClient(t *testing.T, address string, credentials ...string) *client.Client {
	t.Helper()
	pem, err := os.ReadFile("../../testdata/tls/server.crt")
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(pem)
	c, err := client.DialTLS(address, &client.Options{TLSConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	username, password := mailboxUsername, mailboxPassword
	if len(credentials) == 2 {
		username, password = credentials[0], credentials[1]
	}
	if err := c.Login(username, password).Wait(); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Select("Sent", &imap.SelectOptions{ReadOnly: false}).Wait(); err != nil {
		t.Fatal(err)
	}
	return c
}
func apiCall(t *testing.T, h http.Handler, method, path string, body any, status int) *httptest.ResponseRecorder {
	t.Helper()
	var data []byte
	var err error
	if body != nil {
		data, err = json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
	}
	w := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, bytes.NewReader(data))
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	h.ServeHTTP(w, req)
	if w.Code != status {
		t.Fatalf("%s %s: got %d want %d: %s", method, path, w.Code, status, w.Body.String())
	}
	return w
}
func testRaw(id, testID, to string) []byte {
	return []byte("From: Sender <sender@example.test>\r\nTo: " + to + "\r\nMessage-ID: <" + id + ">\r\nX-Test-ID: " + testID + "\r\nSubject: Hello " + testID + "\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nHello world\r\n")
}
func appendTest(t *testing.T, b *Account, id string) *storedMessage {
	t.Helper()
	m, err := b.append(testRaw(id, id, "recipient@example.test"), appendOptions{Folder: "Sent"})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestSMTPHTTPIMAPRoundTripAndReadFlags(t *testing.T) {
	a := testStore(t, defaultConfig())
	h := controlHandler(a)
	wire := smtpWire(t, smtpAddress(t, a))
	smtpReply(t, wire, "MAIL FROM:<sender@example.test>", 250)
	smtpReply(t, wire, "RCPT TO:<recipient@example.test>", 250)
	raw := testRaw("roundtrip@test", "roundtrip", "recipient@example.test")
	smtpReply(t, wire, "DATA", 354)
	writer := wire.DotWriter()
	writer.Write(raw)
	writer.Close()
	smtpReply(t, wire, "", 250)
	m := a.snapshot()[0]
	for _, suffix := range []string{"", "/raw", "/headers", "/view"} {
		apiCall(t, h, "GET", defaultAPI+"/messages/"+m.ID+suffix, nil, 200)
	}
	if hasFlag(a.get(m.ID).Flags, string(imap.FlagSeen)) {
		t.Fatal("HTTP inspection marked message as read")
	}
	c := imapClient(t, imapAddress(t, a.service))
	set := imap.UIDSetNum(imap.UID(m.UID))
	for _, peek := range []bool{true, false} {
		section := &imap.FetchItemBodySection{Peek: peek}
		messages, err := c.Fetch(set, &imap.FetchOptions{BodySection: []*imap.FetchItemBodySection{section}, Flags: true}).Collect()
		if err != nil {
			t.Fatal(err)
		}
		if len(messages) != 1 || !bytes.Equal(messages[0].FindBodySection(section), raw) {
			t.Fatal("MIME changed or missing")
		}
		if seen := hasFlag(a.get(m.ID).Flags, string(imap.FlagSeen)); seen == peek {
			t.Fatalf("peek %v: seen %v", peek, seen)
		}
	}
	apiCall(t, h, "PATCH", defaultAPI+"/messages/"+m.ID, map[string]any{"flags": []string{}}, 200)
	if _, err := c.Select("Sent", &imap.SelectOptions{ReadOnly: true}).Wait(); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Fetch(set, &imap.FetchOptions{BodySection: []*imap.FetchItemBodySection{{}}}).Collect(); err != nil {
		t.Fatal(err)
	}

	if hasFlag(a.get(m.ID).Flags, string(imap.FlagSeen)) {
		t.Fatal("EXAMINE body marked seen")
	}
}

func TestSendmailHelperVerifiesSTARTTLS(t *testing.T) {
	cfg := defaultConfig()
	cfg.SMTPTLS = "starttls"
	cfg.RequireTLS = true
	cfg.Cert = "../../testdata/tls/server.crt"
	cfg.Key = "../../testdata/tls/server.key"
	a := testStore(t, cfg)
	if err := submitMail(smtpAddress(t, a), "sender@example.test", []string{"recipient@example.test"}, testRaw("sendmail@test", "sendmail", "recipient@example.test"), cfg.Cert); err != nil {
		t.Fatal(err)
	}
	if len(a.snapshot()) != 1 {
		t.Fatal("sendmail did not store mail")
	}
}
