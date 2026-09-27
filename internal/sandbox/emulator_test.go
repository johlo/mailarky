package sandbox

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/client"
	"github.com/emersion/go-imap/server"
)

func testMailbox(t *testing.T) (*mailboxBackend, *client.Client, http.Handler) {
	t.Helper()
	b, err := newMailboxBackend()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { b.Close() })
	cert, err := tls.LoadX509KeyPair("../../testdata/tls/server.crt", "../../testdata/tls/server.key")
	if err != nil {
		t.Fatal(err)
	}
	pem, err := os.ReadFile("../../testdata/tls/server.crt")
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(pem) {
		t.Fatal("invalid test certificate")
	}
	srv := server.New(b)
	srv.TLSConfig = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	l, err := tls.Listen("tcp", "127.0.0.1:0", srv.TLSConfig)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- srv.Serve(l) }()
	t.Cleanup(func() { srv.Close(); l.Close(); <-done })
	c, err := client.DialTLS(l.Addr().String(), &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12})
	if err != nil {
		t.Fatal(err)
	}
	c.Timeout = 5 * time.Second
	t.Cleanup(func() { c.Logout() })
	return b, c, controlHandler(b)
}

func seedMessage(handler http.Handler, data string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/messages", strings.NewReader(data)))
	return w
}

func TestSeededMessageCanBeReadOverVerifiedTLS(t *testing.T) {
	b, c, handler := testMailbox(t)
	if err := c.Login(mailboxUsername, "wrong"); err == nil {
		t.Fatal("accepted wrong mailbox credentials")
	}
	if err := c.Login(mailboxUsername, mailboxPassword); err != nil {
		t.Fatal(err)
	}
	response := seedMessage(handler, `{
		"folder":"Sent", "from":"Clinic <clinic@example.test>",
		"to":["Patient <patient@example.test>"], "cc":["copy@example.test"],
		"bcc":["blind@example.test"], "subject":"Äldre brev", "message_id":"<history@example.test>",
		"date":"2020-01-02T05:04:05+02:00", "internal_date":"2021-02-03T04:05:06Z",
		"body":"BODY IS PRESENT IN THE TEST MAILBOX", "flags":["\\Seen"]
	}`)
	if response.Code != http.StatusCreated {
		t.Fatalf("seed: %d %s", response.Code, response.Body.String())
	}
	var result map[string]string
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil || result["message_id"] != "history@example.test" {
		t.Fatalf("response = %v, %v", result, err)
	}
	box, err := c.Select("Sent", true)
	if err != nil || box.Messages != 1 || box.UidNext != 2 || box.UidValidity != b.user.boxes["Sent"].validity {
		t.Fatalf("mailbox = %+v, %v", box, err)
	}
	criteria := imap.NewSearchCriteria()
	criteria.Header.Add("TO", "patient@example.test")
	uids, err := c.UidSearch(criteria)
	if err != nil || len(uids) != 1 || uids[0] != 1 {
		t.Fatalf("search = %v, %v", uids, err)
	}
	set := new(imap.SeqSet)
	set.AddNum(uids...)
	ch := make(chan *imap.Message, 1)
	if err := c.UidFetch(set, []imap.FetchItem{imap.FetchEnvelope, imap.FetchInternalDate, imap.FetchFlags}, ch); err != nil {
		t.Fatal(err)
	}
	msg := <-ch
	e := msg.Envelope
	if e.Subject != "Äldre brev" || e.MessageId != "<history@example.test>" || e.From[0].Address() != mailboxUsername ||
		e.To[0].Address() != "patient@example.test" || e.Cc[0].Address() != "copy@example.test" || e.Bcc[0].Address() != "blind@example.test" ||
		!e.Date.Equal(time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)) || !msg.InternalDate.Equal(time.Date(2021, 2, 3, 4, 5, 6, 0, time.UTC)) {
		t.Fatalf("unexpected message: %+v / %+v", msg, e)
	}
	if len(msg.Body) != 0 || len(msg.Flags) != 1 || msg.Flags[0] != imap.SeenFlag {
		t.Fatalf("metadata fetch changed flags or returned a body: %+v", msg)
	}
}

func TestConcurrentFixtureWritesAndIMAPReads(t *testing.T) {
	_, c, handler := testMailbox(t)
	if err := c.Login(mailboxUsername, mailboxPassword); err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(handler)
	defer httpServer.Close()
	done := make(chan error, 1)
	go func() {
		for i := 0; i < 20; i++ {
			response, err := http.Post(httpServer.URL+"/messages", "application/json", strings.NewReader(`{"from":"patient@example.test","to":["clinic@example.test"]}`))
			if err != nil {
				done <- err
				return
			}
			io.Copy(io.Discard, response.Body)
			response.Body.Close()
			if response.StatusCode != http.StatusCreated {
				done <- fmt.Errorf("seed status: %d", response.StatusCode)
				return
			}
		}
		done <- nil
	}()
	for i := 0; i < 20; i++ {
		if _, err := c.Select("INBOX", true); err != nil {
			t.Fatal(err)
		}
		if _, err := c.UidSearch(imap.NewSearchCriteria()); err != nil {
			t.Fatal(err)
		}
		ch := make(chan *imap.Message, 20)
		set := new(imap.SeqSet)
		set.AddRange(1, 20)
		if err := c.UidFetch(set, []imap.FetchItem{imap.FetchEnvelope, imap.FetchFlags}, ch); err != nil {
			t.Fatal(err)
		}
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	box, err := c.Select("INBOX", true)
	if err != nil || box.Messages != 20 || box.UidNext != 21 {
		t.Fatalf("mailbox after concurrent writes = %+v, %v", box, err)
	}
}

func TestBadFixturesAreRejectedWithoutAppending(t *testing.T) {
	b, _, handler := testMailbox(t)
	for _, data := range []string{
		`{`,
		`{"from":"patient@example.test","to":["clinic@example.test"],"folder":"missing"}`,
		`{"from":"patient@example.test","to":["clinic@example.test"],"date":"not-a-date"}`,
		`{"from":"patient@example.test","to":["clinic@example.test"],"subject":"bad\r\nX-Injected: yes"}`,
		`{"from":"not-an-address","to":["clinic@example.test"]}`,
		`{"from":"patient@example.test","to":["clinic@example.test"],"message_id":"<>"}`,
		`{"from":"patient@example.test"}`,
	} {
		if response := seedMessage(handler, data); response.Code != http.StatusBadRequest {
			t.Fatalf("bad fixture returned %d: %s", response.Code, data)
		}
	}
	status, err := b.user.boxes["INBOX"].Status([]imap.StatusItem{imap.StatusMessages})
	if err != nil || status.Messages != 0 {
		t.Fatalf("bad fixtures changed mailbox: %+v, %v", status, err)
	}
}
