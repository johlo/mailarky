package sandbox

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/smtp"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/backend"
	"github.com/emersion/go-imap/client"
	"github.com/emersion/go-imap/server"
)

func testStore(t *testing.T, c configuration) *mailboxBackend {
	t.Helper()
	b, err := openMailbox(c)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { b.Close() })
	return b
}
func smtpAddress(t *testing.T, b *mailboxBackend, managers ...*mailboxManager) string {
	t.Helper()
	s, err := newSMTPServer(b, b.deliver, managers...)
	if err != nil {
		t.Fatal(err)
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- s.Serve(l) }()
	t.Cleanup(func() { s.Close(); l.Close(); <-done })
	return l.Addr().String()
}
func imapAddress(t *testing.T, b backend.Backend) string {
	t.Helper()
	cert, err := tls.LoadX509KeyPair("../../testdata/tls/server.crt", "../../testdata/tls/server.key")
	if err != nil {
		t.Fatal(err)
	}
	s := server.New(b)
	l, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- s.Serve(l) }()
	t.Cleanup(func() { s.Close(); l.Close(); <-done })
	return l.Addr().String()
}
func imapClient(t *testing.T, address string, credentials ...string) *client.Client {
	t.Helper()
	pem, err := os.ReadFile("../../testdata/tls/server.crt")
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(pem)
	c, err := client.DialTLS(address, &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12})
	if err != nil {
		t.Fatal(err)
	}
	c.Timeout = 5 * time.Second
	t.Cleanup(func() { c.Logout() })
	username, password := mailboxUsername, mailboxPassword
	if len(credentials) == 2 {
		username, password = credentials[0], credentials[1]
	}
	if err := c.Login(username, password); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Select("Sent", false); err != nil {
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
	h.ServeHTTP(w, httptest.NewRequest(method, path, bytes.NewReader(data)))
	if w.Code != status {
		t.Fatalf("%s %s: got %d want %d: %s", method, path, w.Code, status, w.Body.String())
	}
	return w
}
func testRaw(id, testID, to string) []byte {
	return []byte("From: Sender <sender@example.test>\r\nTo: " + to + "\r\nMessage-ID: <" + id + ">\r\nX-Test-ID: " + testID + "\r\nSubject: Hello " + testID + "\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nHello world\r\n")
}
func appendTest(t *testing.T, b *mailboxBackend, id string) *storedMessage {
	t.Helper()
	m, err := b.append(testRaw(id, id, "recipient@example.test"), appendOptions{Folder: "Sent"})
	if err != nil {
		t.Fatal(err)
	}
	return m
}
func awaitHits(t *testing.T, b *mailboxBackend, name string, n uint64) {
	t.Helper()
	until := time.Now().Add(3 * time.Second)
	for time.Now().Before(until) {
		for _, x := range b.toxics.list() {
			if x.Name == name && x.Hits >= n {
				return
			}
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("toxic %s did not fire", name)
}

func TestSMTPToHTTPAndIMAPRoundTrip(t *testing.T) {
	b := testStore(t, defaultConfig())
	address := smtpAddress(t, b)
	imapAddr := imapAddress(t, b)
	h := controlHandler(b)
	raw := testRaw("roundtrip@example.test", "roundtrip", "recipient@example.test")
	if err := smtp.SendMail(address, nil, "sender@example.test", []string{"recipient@example.test", "hidden@example.test"}, raw); err != nil {
		t.Fatal(err)
	}
	var list messagesSummary
	json.Unmarshal(apiCall(t, h, "GET", "/api/v1/messages", nil, 200).Body.Bytes(), &list)
	if list.Total != 1 || list.Unread != 1 || list.Messages[0].MessageID != "roundtrip@example.test" {
		t.Fatalf("list: %+v", list)
	}
	id := list.Messages[0].ID
	if got := apiCall(t, h, "GET", "/api/v1/message/"+id+"/raw", nil, 200).Body.Bytes(); !bytes.Equal(raw, got) {
		t.Fatalf("raw changed: %q", got)
	}
	var detail messageDetail
	json.Unmarshal(apiCall(t, h, "GET", "/api/v1/message/"+id, nil, 200).Body.Bytes(), &detail)
	if len(detail.Bcc) != 1 || detail.Bcc[0].Address != "hidden@example.test" || !detail.Read {
		t.Fatalf("detail: %+v", detail)
	}
	c := imapClient(t, imapAddr)
	set := new(imap.SeqSet)
	set.AddNum(list.Messages[0].UID)
	ch := make(chan *imap.Message, 2)
	section := &imap.BodySectionName{Peek: true}
	if err := c.UidFetch(set, []imap.FetchItem{section.FetchItem(), imap.FetchEnvelope, imap.FetchFlags}, ch); err != nil {
		t.Fatal(err)
	}
	message := <-ch
	if message == nil {
		t.Fatal("no IMAP message")
	}
	got, err := io.ReadAll(message.GetBody(section))
	if err != nil || !bytes.Equal(raw, got) {
		t.Fatalf("IMAP raw: %q %v", got, err)
	}
	if !hasFlag(message.Flags, imap.SeenFlag) {
		t.Fatal("HTTP read did not update IMAP flag")
	}
	if err := c.UidStore(set, imap.FormatFlagsOp(imap.RemoveFlags, true), []interface{}{imap.SeenFlag}, nil); err != nil {
		t.Fatal(err)
	}
	if hasFlag(b.get(id).Flags, imap.SeenFlag) {
		t.Fatal("IMAP flag did not update HTTP state")
	}
}

func TestSMTPMessageScopedToxicsAndLifecycle(t *testing.T) {
	b := testStore(t, defaultConfig())
	address := smtpAddress(t, b)
	h := controlHandler(b)
	apiCall(t, h, "POST", "/api/v1/toxics", map[string]any{"name": "test-a", "type": "smtp_reject", "selector": map[string]any{"headers": map[string]string{"X-Test-ID": "a"}}, "attributes": map[string]any{"code": 451}}, 201)
	const workers = 24
	var group sync.WaitGroup
	errs := make(chan error, workers)
	for i := 0; i < workers; i++ {
		group.Add(1)
		go func(i int) {
			defer group.Done()
			scope := "a"
			if i%2 == 1 {
				scope = "b"
			}
			err := smtp.SendMail(address, nil, "sender@example.test", []string{"recipient@example.test"}, testRaw(fmt.Sprintf("concurrent-%d@test", i), scope, "recipient@example.test"))
			if scope == "a" && (err == nil || !strings.Contains(err.Error(), "451")) {
				errs <- fmt.Errorf("target mail: %v", err)
			}
			if scope == "b" && err != nil {
				errs <- fmt.Errorf("unrelated mail: %w", err)
			}
		}(i)
	}
	group.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if len(b.snapshot()) != workers/2 {
		t.Fatalf("stored %d messages", len(b.snapshot()))
	}
	apiCall(t, h, "PATCH", "/api/v1/toxics/test-a", map[string]bool{"enabled": false}, 200)
	if err := smtp.SendMail(address, nil, "sender@example.test", []string{"recipient@example.test"}, testRaw("disabled@test", "a", "recipient@example.test")); err != nil {
		t.Fatal(err)
	}
	apiCall(t, h, "PATCH", "/api/v1/toxics/test-a", map[string]bool{"enabled": true}, 200)
	if err := smtp.SendMail(address, nil, "sender@example.test", []string{"recipient@example.test"}, testRaw("enabled@test", "a", "recipient@example.test")); err == nil {
		t.Fatal("enabled toxic did not reject")
	}
	apiCall(t, h, "DELETE", "/api/v1/toxics/test-a", nil, 204)
	apiCall(t, h, "GET", "/api/v1/toxics/test-a", nil, 404)
	if err := smtp.SendMail(address, nil, "sender@example.test", []string{"recipient@example.test"}, testRaw("deleted@test", "a", "recipient@example.test")); err != nil {
		t.Fatal(err)
	}
}

func TestSMTPDelayDoesNotBlockOtherTests(t *testing.T) {
	b := testStore(t, defaultConfig())
	address := smtpAddress(t, b)
	if err := b.toxics.put(toxic{Name: "delay-a", Type: "smtp_delay", Enabled: true, Selector: messageSelector{MessageID: "slow@test"}, Attributes: toxicAttributes{DelayMS: 1000}}, true); err != nil {
		t.Fatal(err)
	}
	slow := make(chan error, 1)
	go func() {
		slow <- smtp.SendMail(address, nil, "sender@example.test", []string{"recipient@example.test"}, testRaw("slow@test", "a", "recipient@example.test"))
	}()
	awaitHits(t, b, "delay-a", 1)
	fast := make(chan error, 1)
	go func() {
		fast <- smtp.SendMail(address, nil, "sender@example.test", []string{"recipient@example.test"}, testRaw("fast@test", "b", "recipient@example.test"))
	}()
	select {
	case err := <-fast:
		if err != nil {
			t.Fatal(err)
		}
	case <-slow:
		t.Fatal("unrelated SMTP transaction waited for toxic delay")
	case <-time.After(3 * time.Second):
		t.Fatal("SMTP blocked")
	}
	if err := <-slow; err != nil {
		t.Fatal(err)
	}
}

func TestToxicValidationAndAtomicHitLimit(t *testing.T) {
	b := testStore(t, defaultConfig())
	h := controlHandler(b)
	for _, body := range []string{
		`{"name":"global","type":"smtp_reject","selector":{},"attributes":{"code":451}}`,
		`{"name":"folder","type":"imap_hide","selector":{"folder":"Sent"}}`,
		`{"name":"wildcard","type":"imap_hide","selector":{"to":"*@example.test"}}`,
		`{"name":"early","type":"smtp_delay","selector":{"headers":{"X-Test-ID":"a"}},"attributes":{"stage":"sender"}}`,
		`{"name":"unknown","type":"smtp_disconnect","selector":{"message_id":"a"}}`,
		`{"name":"invalid","type":"smtp_reject","selector":{"message_id":"a"},"attributes":{"code":200}}`,
	} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("POST", "/api/v1/toxics", strings.NewReader(body)))
		if w.Code != 400 {
			t.Fatalf("accepted %s", body)
		}
	}
	apiCall(t, h, "PUT", "/api/v1/chaos", map[string]any{"Enabled": true}, 400)
	raw := appendTest(t, b, "limited@test")
	if err := b.toxics.put(toxic{Name: "limited", Type: "imap_hide", Enabled: true, MaxHits: 7, Selector: messageSelector{MessageID: "limited@test"}}, true); err != nil {
		t.Fatal(err)
	}
	var count atomic.Uint64
	var group sync.WaitGroup
	for i := 0; i < 100; i++ {
		group.Add(1)
		go func() { defer group.Done(); count.Add(uint64(len(b.toxics.claim("imap", "", raw)))) }()
	}
	group.Wait()
	if count.Load() != 7 {
		t.Fatalf("fired %d times", count.Load())
	}
	b.toxics.remove("limited")
	if err := b.toxics.put(toxic{Name: "limited", Type: "imap_hide", Enabled: true, Selector: messageSelector{MessageID: "limited@test"}}, true); err != nil {
		t.Fatal(err)
	}
	if len(b.toxics.claim("imap", "", raw)) != 1 {
		t.Fatal("recreated toxic fired twice")
	}
}

func TestIMAPToxicsDoNotAffectOtherTestsOrStoredMIME(t *testing.T) {
	b := testStore(t, defaultConfig())
	a := appendTest(t, b, "a@test")
	other := appendTest(t, b, "b@test")
	addr := imapAddress(t, b)
	ca := imapClient(t, addr)
	cb := imapClient(t, addr)
	if err := b.toxics.put(toxic{Name: "slow-a", Type: "imap_delay", Enabled: true, Selector: messageSelector{MessageID: "a@test"}, Attributes: toxicAttributes{DelayMS: 1000}}, true); err != nil {
		t.Fatal(err)
	}
	criteria := func(id string) *imap.SearchCriteria {
		c := imap.NewSearchCriteria()
		c.Header.Add("Message-ID", "<"+id+">")
		return c
	}
	slow := make(chan error, 1)
	go func() { _, err := ca.UidSearch(criteria("a@test")); slow <- err }()
	awaitHits(t, b, "slow-a", 1)
	fast := make(chan error, 1)
	go func() {
		ids, err := cb.UidSearch(criteria("b@test"))
		if err == nil && (len(ids) != 1 || ids[0] != other.UID) {
			err = fmt.Errorf("unexpected IDs %v", ids)
		}
		fast <- err
	}()
	select {
	case err := <-fast:
		if err != nil {
			t.Fatal(err)
		}
	case <-slow:
		t.Fatal("unrelated IMAP SEARCH waited on toxic")
	case <-time.After(3 * time.Second):
		t.Fatal("IMAP blocked")
	}
	if err := <-slow; err != nil {
		t.Fatal(err)
	}
	b.toxics.remove("slow-a")
	if err := b.toxics.put(toxic{Name: "hide-a", Type: "imap_hide", Enabled: true, Selector: messageSelector{MessageID: "a@test"}}, true); err != nil {
		t.Fatal(err)
	}
	ids, err := cb.UidSearch(imap.NewSearchCriteria())
	if err != nil || len(ids) != 1 || ids[0] != other.UID {
		t.Fatalf("hide search: %v %v", ids, err)
	}
	set := new(imap.SeqSet)
	set.AddRange(1, 0)
	ch := make(chan *imap.Message, 2)
	if err := cb.UidFetch(set, []imap.FetchItem{imap.FetchEnvelope}, ch); err != nil {
		t.Fatal(err)
	}
	if got := <-ch; got == nil || got.Uid != other.UID {
		t.Fatalf("hide fetch: %+v", got)
	}
	if got := <-ch; got != nil {
		t.Fatal("hidden message returned")
	}
	b.toxics.remove("hide-a")
	if err := b.toxics.put(toxic{Name: "header-a", Type: "imap_replace_header", Enabled: true, Selector: messageSelector{MessageID: "a@test"}, Attributes: toxicAttributes{Header: "Subject", Value: "corrupt"}}, true); err != nil {
		t.Fatal(err)
	}
	ch = make(chan *imap.Message, 2)
	if err := cb.UidFetch(set, []imap.FetchItem{imap.FetchEnvelope}, ch); err != nil {
		t.Fatal(err)
	}
	for msg := range ch {
		if msg.Uid == a.UID && msg.Envelope.Subject != "corrupt" {
			t.Fatal("target header was not changed")
		}
		if msg.Uid == other.UID && msg.Envelope.Subject == "corrupt" {
			t.Fatal("unrelated header was changed")
		}
	}
	if !bytes.Equal(a.Raw, b.get(a.ID).Raw) {
		t.Fatal("toxic mutated persisted MIME")
	}
}

func TestPersistenceRetentionAndUIDs(t *testing.T) {
	c := defaultConfig()
	c.Database = filepath.Join(t.TempDir(), "mail.db")
	c.MaxMessages = 2
	b, err := openMailbox(c)
	if err != nil {
		t.Fatal(err)
	}
	a := appendTest(t, b, "first@test")
	second := appendTest(t, b, "second@test")
	third := appendTest(t, b, "third@test")
	validity := b.state.Folders["Sent"].Validity
	if b.get(a.ID) != nil {
		t.Fatal("retention did not evict oldest")
	}
	apiCall(t, controlHandler(b), "DELETE", "/api/v1/messages", map[string]any{"IDs": []string{second.ID}}, 200)
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	b = testStore(t, c)
	if len(b.snapshot()) != 1 || !bytes.Equal(b.get(third.ID).Raw, third.Raw) {
		t.Fatal("restart lost MIME or resurrected deletions")
	}
	fourth := appendTest(t, b, "fourth@test")
	if fourth.UID != 4 || b.state.Folders["Sent"].Validity != validity {
		t.Fatal("restart changed UID identity")
	}
	b.config.MaxAge = time.Nanosecond
	time.Sleep(time.Millisecond)
	if err := b.prune(); err != nil {
		t.Fatal(err)
	}
	if len(b.snapshot()) != 0 {
		t.Fatal("age retention failed")
	}
}

func TestSendAPIMIMEAttachmentsSearchAndTags(t *testing.T) {
	b := testStore(t, defaultConfig())
	h := controlHandler(b)
	body := sendRequest{From: sendAddress{Name: "Sender", Email: "sender@example.test"}, To: []sendAddress{{Email: "patient+test-a@example.test"}}, Bcc: []string{"blind@example.test"}, Subject: "Äldre order", Text: "plain world", HTML: `<div style="display:flex">Hello <b>world</b></div>`, Headers: map[string]string{"X-Tags": "sent,order", "Message-Id": "<mime@test>"}, Attachments: []sendAttachment{{Filename: "test.txt", Content: base64.StdEncoding.EncodeToString([]byte("attachment payload"))}}}
	var created map[string]string
	json.Unmarshal(apiCall(t, h, "POST", "/api/v1/send", body, 200).Body.Bytes(), &created)
	id := created["ID"]
	if id == "" {
		t.Fatal(created)
	}
	var d messageDetail
	json.Unmarshal(apiCall(t, h, "GET", "/api/v1/message/"+id, nil, 200).Body.Bytes(), &d)
	if d.Subject != "Äldre order" || !strings.Contains(d.Text, "plain world") || len(d.Attachments) != 1 || len(d.Bcc) != 1 {
		t.Fatalf("MIME parse: %+v", d)
	}
	if got := apiCall(t, h, "GET", "/api/v1/message/"+id+"/part/"+d.Attachments[0].PartID, nil, 200).Body.String(); got != "attachment payload" {
		t.Fatal(got)
	}
	for _, q := range []string{`subject:"äldre order" !to:someone has:attachment is:read tag:test-a`, `bcc:blind@example.test message-id:mime@test larger:10 smaller:1M`, `world -subject:missing`} {
		var list messagesSummary
		json.Unmarshal(apiCall(t, h, "GET", "/api/v1/search?query="+url.QueryEscape(q), nil, 200).Body.Bytes(), &list)
		if list.Count != 1 {
			t.Fatalf("search %q = %d", q, list.Count)
		}
	}
	apiCall(t, h, "PUT", "/api/v1/tags", map[string]any{"IDs": []string{id}, "Tags": []string{"renameme"}}, 200)
	apiCall(t, h, "PUT", "/api/v1/tags/renameme", map[string]string{"Name": "renamed"}, 200)
	if !containsString(b.get(id).Tags, "renamed") {
		t.Fatal("tag rename failed")
	}
	apiCall(t, h, "DELETE", "/api/v1/tags/renamed", nil, 200)
	var check struct{ Warnings []compatibilityWarning }
	json.Unmarshal(apiCall(t, h, "GET", "/api/v1/message/"+id+"/html-check", nil, 200).Body.Bytes(), &check)
	found := false
	for _, v := range check.Warnings {
		if v.Slug == "css-display-flex" && v.Score.Unsupported > 0 {
			found = true
		}
	}
	if !found {
		t.Fatal("HTML checker did not report flex support")
	}
	apiCall(t, h, "DELETE", "/api/v1/search?query="+url.QueryEscape("message-id:mime@test"), nil, 200)
	if len(b.snapshot()) != 0 {
		t.Fatal("search delete failed")
	}
}

func TestSMTPAuthenticationAndSTARTTLS(t *testing.T) {
	c := defaultConfig()
	c.SMTPAuthFile = filepath.Join(t.TempDir(), "auth")
	os.WriteFile(c.SMTPAuthFile, []byte("tester:secret\n"), 0600)
	c.SMTPTLS = "starttls"
	c.RequireTLS = true
	c.Cert = "../../testdata/tls/server.crt"
	c.Key = "../../testdata/tls/server.key"
	b := testStore(t, c)
	addr := smtpAddress(t, b)
	conn, err := smtp.Dial(addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.Mail("sender@example.test"); err == nil {
		t.Fatal("accepted unauthenticated sender")
	}
	pem, _ := os.ReadFile(c.Cert)
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(pem)
	if err := conn.StartTLS(&tls.Config{RootCAs: roots, ServerName: "127.0.0.1", MinVersion: tls.VersionTLS12}); err != nil {
		t.Fatal(err)
	}
	if err := conn.Auth(smtp.PlainAuth("", "tester", "secret", "127.0.0.1")); err != nil {
		t.Fatal(err)
	}
	if err := conn.Mail("sender@example.test"); err != nil {
		t.Fatal(err)
	}
	if err := conn.Rcpt("recipient@example.test"); err != nil {
		t.Fatal(err)
	}
	w, err := conn.Data()
	if err != nil {
		t.Fatal(err)
	}
	w.Write(testRaw("auth@test", "a", "recipient@example.test"))
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if b.snapshot()[0].Username != "tester" {
		t.Fatal("authentication identity not stored")
	}
}

func TestRelayForwardAndRecipientRestrictions(t *testing.T) {
	destination := testStore(t, defaultConfig())
	addr := smtpAddress(t, destination)
	host, port, _ := net.SplitHostPort(addr)
	p, _ := strconv.Atoi(port)
	c := defaultConfig()
	c.Relay = relayConfig{Host: host, Port: p, AllowedRecipients: `^allowed@`, BlockedRecipients: `^blocked@`}
	c.RelayMatching = `@forward.test$`
	b := testStore(t, c)
	address := smtpAddress(t, b)
	if err := smtp.SendMail(address, nil, "sender@example.test", []string{"allowed@forward.test", "ignored@example.test"}, testRaw("relay@test", "a", "allowed@forward.test")); err != nil {
		t.Fatal(err)
	}
	got := destination.snapshot()
	if len(got) != 1 || len(got[0].EnvelopeTo) != 1 || got[0].EnvelopeTo[0] != "allowed@forward.test" {
		t.Fatalf("auto relay: %+v", got)
	}
	id := b.snapshot()[0].ID
	apiCall(t, controlHandler(b), "POST", "/api/v1/message/"+id+"/release", map[string]any{"To": []string{"blocked@example.test"}}, 400)
	apiCall(t, controlHandler(b), "POST", "/api/v1/message/"+id+"/release", map[string]any{"To": []string{"allowed@example.test"}}, 200)
	got = destination.snapshot()
	if len(got) != 2 || got[1].Detail.MessageID == "relay@test" {
		t.Fatal("manual relay did not replace Message-ID")
	}
	if b.get(id).Detail.MessageID != "relay@test" {
		t.Fatal("release mutated original")
	}
	b.config.Forward = relayConfig{Host: host, Port: p, To: "copy@example.test"}
	if err := b.deliver(context.Background(), b.get(id)); err != nil {
		t.Fatal(err)
	}
	if len(destination.snapshot()) != 4 {
		t.Fatal("forward/relay did not both deliver")
	}
}

func TestWebhooksWebsocketAndMetrics(t *testing.T) {
	webhooks := make(chan messageSummary, 2)
	var calls atomic.Int32
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(503)
			return
		}
		var m messageSummary
		json.NewDecoder(r.Body).Decode(&m)
		webhooks <- m
	}))
	defer receiver.Close()
	c := defaultConfig()
	c.WebhookURL = receiver.URL
	c.WebhookLimit = 0
	c.EnableMetrics = true
	b := testStore(t, c)
	stop := b.startWorkers(context.Background())
	defer stop()
	hs := httptest.NewServer(controlHandler(b))
	defer hs.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ws, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(hs.URL, "http")+"/api/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.CloseNow()
	// Wait for subscription establishment, rather than relying on network timing.
	for deadline := time.Now().Add(time.Second); ; {
		b.mu.RLock()
		n := len(b.subscribers)
		b.mu.RUnlock()
		if n > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("websocket subscription missing")
		}
		time.Sleep(time.Millisecond)
	}
	m, err := b.append(testRaw("notify@test", "n", "recipient@example.test"), appendOptions{Folder: "Sent", Notify: true})
	if err != nil {
		t.Fatal(err)
	}
	_, data, err := ws.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var event struct {
		Type string
		Data messageSummary
	}
	json.Unmarshal(data, &event)
	if event.Type != "new" || event.Data.ID != m.ID {
		t.Fatalf("event %s", data)
	}
	select {
	case got := <-webhooks:
		if got.ID != m.ID {
			t.Fatal(got)
		}
	case <-ctx.Done():
		t.Fatal("webhook was not retried")
	}
	if got := apiCall(t, controlHandler(b), "GET", "/metrics", nil, 200).Body.String(); !strings.Contains(got, "mail_sandbox_messages 1") {
		t.Fatal(got)
	}
}

func TestLinkCheckerBlocksPrivateHostsAndFollowsRedirects(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, "/ok", 302)
			return
		}
		w.WriteHeader(200)
	}))
	defer server.Close()
	b := testStore(t, defaultConfig())
	raw, _, _, err := (sendRequest{From: sendAddress{Email: "sender@example.test"}, To: []sendAddress{{Email: "recipient@example.test"}}, HTML: `<a href="` + server.URL + `/redirect">link</a>`}).mimeMessage()
	if err != nil {
		t.Fatal(err)
	}
	m, err := b.append(raw, appendOptions{Folder: "Sent"})
	if err != nil {
		t.Fatal(err)
	}
	h := controlHandler(b)
	var result struct {
		Errors int
		Links  []linkResult
	}
	json.Unmarshal(apiCall(t, h, "GET", "/api/v1/message/"+m.ID+"/link-check", nil, 200).Body.Bytes(), &result)
	if result.Errors != 1 {
		t.Fatal("private host allowed by default")
	}
	b.config.AllowInternalHTTP = true
	json.Unmarshal(apiCall(t, h, "GET", "/api/v1/message/"+m.ID+"/link-check?follow=true", nil, 200).Body.Bytes(), &result)
	if result.Errors != 0 || result.Links[0].StatusCode != 200 {
		t.Fatalf("follow: %+v", result)
	}
}

func TestConfigurationAndHTTPAuthentication(t *testing.T) {
	t.Setenv("MP_ENABLE_CHAOS", "true")
	if _, err := loadConfig(nil); err == nil {
		t.Fatal("global chaos accepted")
	}
	t.Setenv("MP_ENABLE_CHAOS", "false")
	t.Setenv("MP_SMTP_RELAY_MATCHING", "[")
	if _, err := loadConfig(nil); err == nil {
		t.Fatal("bad regex accepted")
	}
	t.Setenv("MP_SMTP_RELAY_MATCHING", "")
	path := filepath.Join(t.TempDir(), "config.yaml")
	os.WriteFile(path, []byte("max_message_bytes: 1024\nmax_messages: 0\n"), 0600)
	t.Setenv("MAIL_SANDBOX_CONFIG", path)
	c, err := loadConfig(nil)
	if err != nil || c.MaxSize != 1024 || c.MaxMessages != 0 {
		t.Fatalf("config %+v %v", c, err)
	}
	c.HTTPAuthFile = filepath.Join(t.TempDir(), "auth")
	os.WriteFile(c.HTTPAuthFile, []byte("admin:secret\n"), 0600)
	c.Webroot = "/mail"
	b := testStore(t, c)
	h := controlHandler(b)
	apiCall(t, h, "GET", "/mail/healthz", nil, 200)
	apiCall(t, h, "GET", "/mail/api/v1/messages", nil, 401)
	r := httptest.NewRequest("GET", "/mail/api/v1/messages", nil)
	r.SetBasicAuth("admin", "secret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
}
