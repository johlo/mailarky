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

	"github.com/johlo/go-imap/v2"
	client "github.com/johlo/go-imap/v2/imapclient"
)

func testMailbox(t *testing.T) (*Account, *client.Client, http.Handler) {
	t.Helper()
	b := testStore(t, defaultConfig())
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
	srv := newIMAPServer(b.service)
	tlsConfig := &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	l, err := tls.Listen("tcp", "127.0.0.1:0", tlsConfig)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- srv.Serve(l) }()
	t.Cleanup(func() { srv.Close(); l.Close(); <-done })
	c, err := client.DialTLS(l.Addr().String(), &client.Options{TLSConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return b, c, controlHandler(b)
}

func seedMessage(handler http.Handler, data string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, defaultAPI+"/messages", strings.NewReader(data))
	req.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(w, req)
	return w
}

func TestSeededMessageCanBeReadOverVerifiedTLS(t *testing.T) {
	b, c, handler := testMailbox(t)
	if err := c.Login(mailboxUsername, "wrong").Wait(); err == nil {
		t.Fatal("accepted wrong mailbox credentials")
	}
	if err := c.Login(mailboxUsername, mailboxPassword).Wait(); err != nil {
		t.Fatal(err)
	}
	response := seedMessage(handler, `{
		"folder":"Sent", "from":"Test User <user@example.test>",
		"to":["Recipient <recipient@example.test>"], "cc":["copy@example.test"],
		"bcc":["blind@example.test"], "subject":"Äldre brev", "message_id":"<history@example.test>",
		"date":"2020-01-02T05:04:05+02:00", "internal_date":"2021-02-03T04:05:06Z",
		"body":"BODY IS PRESENT IN THE TEST MAILBOX", "flags":["\\Seen"]
	}`)
	if response.Code != http.StatusCreated {
		t.Fatalf("seed: %d %s", response.Code, response.Body.String())
	}
	var result messageDetail
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil || result.MessageID != "history@example.test" {
		t.Fatalf("response = %v, %v", result, err)
	}
	box, err := c.Select("Sent", &imap.SelectOptions{ReadOnly: true}).Wait()
	if err != nil || box.NumMessages != 1 || box.UIDNext != 2 || box.UIDValidity != b.state.Folders["Sent"].Validity {
		t.Fatalf("mailbox = %+v, %v", box, err)
	}
	criteria := new(imap.SearchCriteria)
	criteria.Header = append(criteria.Header, imap.SearchCriteriaHeaderField{Key: "TO", Value: "recipient@example.test"})
	resultSet, err := c.UIDSearch(criteria, nil).Wait()
	var uids []imap.UID
	if resultSet != nil {
		uids = resultSet.AllUIDs()
	}
	if err != nil || len(uids) != 1 || uids[0] != 1 {
		t.Fatalf("search = %v, %v", uids, err)
	}
	messages, err := c.Fetch(imap.UIDSetNum(uids...), &imap.FetchOptions{Envelope: true, InternalDate: true, Flags: true}).Collect()
	if err != nil || len(messages) != 1 {
		t.Fatal("fetch failed", err)
	}
	msg := messages[0]

	e := msg.Envelope
	if e.Subject != "Äldre brev" || e.MessageID != "history@example.test" || e.From[0].Addr() != mailboxUsername ||
		e.To[0].Addr() != "recipient@example.test" || e.Cc[0].Addr() != "copy@example.test" || e.Bcc[0].Addr() != "blind@example.test" ||
		!e.Date.Equal(time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)) || !msg.InternalDate.Equal(time.Date(2021, 2, 3, 4, 5, 6, 0, time.UTC)) {
		t.Fatalf("unexpected message: %+v / %+v", msg, e)
	}
	if len(msg.BodySection) != 0 || len(msg.Flags) != 1 || msg.Flags[0] != imap.FlagSeen {
		t.Fatalf("metadata fetch changed flags or returned a body: %+v", msg)
	}
}

func TestConcurrentFixtureWritesAndIMAPReads(t *testing.T) {
	_, c, handler := testMailbox(t)
	if err := c.Login(mailboxUsername, mailboxPassword).Wait(); err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(handler)
	defer httpServer.Close()
	done := make(chan error, 1)
	go func() {
		for i := 0; i < 20; i++ {
			response, err := http.Post(httpServer.URL+defaultAPI+"/messages", "application/json", strings.NewReader(`{"from":"recipient@example.test","to":["user@example.test"]}`))
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
		if _, err := c.Select("INBOX", &imap.SelectOptions{ReadOnly: true}).Wait(); err != nil {
			t.Fatal(err)
		}
		if _, err := c.UIDSearch(new(imap.SearchCriteria), nil).Wait(); err != nil {
			t.Fatal(err)
		}
		set := imap.UIDSet{{Start: 1, Stop: 20}}
		if _, err := c.Fetch(set, &imap.FetchOptions{Envelope: true, Flags: true}).Collect(); err != nil {
			t.Fatal(err)
		}

	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	box, err := c.Select("INBOX", &imap.SelectOptions{ReadOnly: true}).Wait()
	if err != nil || box.NumMessages != 20 || box.UIDNext != 21 {
		t.Fatalf("mailbox after concurrent writes = %+v, %v", box, err)
	}
}

func TestBadFixturesAreRejectedWithoutAppending(t *testing.T) {
	b, _, handler := testMailbox(t)
	for _, data := range []string{
		`{`,
		`{"from":"recipient@example.test","to":["user@example.test"],"folder":"missing"}`,
		`{"from":"recipient@example.test","to":["user@example.test"],"date":"not-a-date"}`,
		`{"from":"recipient@example.test","to":["user@example.test"],"subject":"bad\r\nX-Injected: yes"}`,
		`{"from":"not-an-address","to":["user@example.test"]}`,
		`{"from":"recipient@example.test","to":["user@example.test"],"message_id":"<>"}`,
		`{"from":"recipient@example.test"}`,
	} {
		if response := seedMessage(handler, data); response.Code != http.StatusBadRequest {
			t.Fatalf("bad fixture returned %d: %s", response.Code, data)
		}
	}
	box, _ := b.user.GetMailbox("INBOX")
	status, err := box.Status()
	if err != nil || status.NumMessages != 0 {
		t.Fatalf("bad fixtures changed mailbox: %+v, %v", status, err)
	}
}
