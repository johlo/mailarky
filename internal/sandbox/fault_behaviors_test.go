package sandbox

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/johlo/go-imap/v2"
)

func TestIMAPContentDelayDoesNotBlockAnotherMessage(t *testing.T) {
	a := testStore(t, defaultConfig())
	appendTest(t, a, "slow@test")
	other := appendTest(t, a, "fast@test")
	addr := imapAddress(t, a.service)
	slowClient := imapClient(t, addr)
	fastClient := imapClient(t, addr)
	f := rule("slow", "imap", "UID SEARCH", "content", map[string]any{"type": "delay", "delay_ms": 250})
	f["filter"] = map[string]any{"message": map[string]string{"message_id": "slow@test"}}
	installFault(t, a, false, f)
	criteria := func(id string) *imap.SearchCriteria {
		c := new(imap.SearchCriteria)
		c.Header = append(c.Header, imap.SearchCriteriaHeaderField{Key: "Message-ID", Value: "<" + id + ">"})
		return c
	}
	slow := make(chan error, 1)
	go func() { _, err := slowClient.UIDSearch(criteria("slow@test"), nil).Wait(); slow <- err }()
	awaitFault(t, a.faults, "slow", 1)
	fast := make(chan error, 1)
	go func() {
		ids, err := fastClient.UIDSearch(criteria("fast@test"), nil).Wait()
		if err == nil && (len(ids.AllUIDs()) != 1 || ids.AllUIDs()[0] != imap.UID(other.UID)) {
			err = fmt.Errorf("wrong UIDs %v", ids)
		}
		fast <- err
	}()
	select {
	case err := <-fast:
		if err != nil {
			t.Fatal(err)
		}
	case <-slow:
		t.Fatal("unrelated IMAP search waited for delay")
	}
	if err := <-slow; err != nil {
		t.Fatal(err)
	}
}
func TestIMAPBodyReplacementLeavesPersistedMIMEUntouched(t *testing.T) {
	a := testStore(t, defaultConfig())
	m := appendTest(t, a, "body@test")
	client := imapClient(t, imapAddress(t, a.service))
	replacement := "From: other@example.test\r\nSubject: altered\r\n\r\nreplacement\r\n"
	f := rule("body", "imap", "UID FETCH", "content", map[string]any{"type": "replace_body", "value": replacement})
	f["filter"] = map[string]any{"message": map[string]string{"message_id": "body@test"}}
	installFault(t, a, false, f)
	section := &imap.FetchItemBodySection{Peek: true}
	messages, err := client.Fetch(imap.UIDSetNum(imap.UID(m.UID)), &imap.FetchOptions{BodySection: []*imap.FetchItemBodySection{section}}).Collect()
	if err != nil || len(messages) != 1 {
		t.Fatal("missing fetch", err)
	}
	data := messages[0].FindBodySection(section)
	if string(data) != replacement {
		t.Fatal(string(data))
	}

	if !bytes.Equal(a.get(m.ID).Raw, m.Raw) {
		t.Fatal("replacement changed storage")
	}
}
func TestRawProtocolResponsesAndCommandAfterFaults(t *testing.T) {
	a := testStore(t, defaultConfig())
	installFault(t, a, true, rule("reply", "smtp", "NOOP", "before", map[string]any{"type": "response", "response": "299 unusual reply"}))
	smtp := smtpWire(t, smtpAddress(t, a))
	smtpReply(t, smtp, "NOOP", 299)
	m := appendTest(t, a, "flags@test")
	w := imapWire(t, imapAddress(t, a.service), mailboxUsername, mailboxPassword)
	imapReply(t, w, "SELECT Sent", "OK")
	f := rule("after", "imap", "UID STORE", "after", map[string]any{"type": "reject", "status": "NO"})
	f["max_hits"] = 1
	installFault(t, a, false, f)
	imapReply(t, w, `UID STORE 1 +FLAGS.SILENT (\Seen)`, "NO")
	if !hasFlag(a.get(m.ID).Flags, `\Seen`) {
		t.Fatal("after fault ran before STORE")
	}
	f = rule("raw", "imap", "UID FETCH", "content", map[string]any{"type": "response", "response": "{tag} NO [UNAVAILABLE] synthetic", "close": true})
	f["filter"] = map[string]any{"message": map[string]string{"message_id": "flags@test"}}
	installFault(t, a, false, f)
	imapReply(t, w, "UID FETCH 1 FLAGS", "NO")
	assertClosed(t, w)
}
func TestIMAPGreetingCapabilitiesAndExpiredRules(t *testing.T) {
	a := testStore(t, defaultConfig())
	installFault(t, a, true, rule("caps", "imap", "CAPABILITY", "after", map[string]any{"type": "capabilities", "omit": []string{"IDLE"}}))
	f := rule("expired", "imap", "LOGIN", "before", map[string]any{"type": "disconnect"})
	f["expires_at"] = time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)
	installFault(t, a, false, f)
	w := faultWire(t, imapAddress(t, a.service), true)
	line, err := w.ReadLine()
	if err != nil || strings.Contains(line, "IDLE") || strings.Contains(line, " MOVE ") {
		t.Fatal(line, err)
	}
	imapReply(t, w, fmt.Sprintf("LOGIN %q %q", mailboxUsername, mailboxPassword), "OK")
	got, _ := a.faults.get("expired")
	if got.Hits != 0 {
		t.Fatal("expired rule claimed")
	}
}

func TestIMAPFetchContentFaultAfterFirstResponse(t *testing.T) {
	for _, command := range []string{"FETCH", "UID FETCH"} {
		for _, action := range []struct {
			name   string
			fields map[string]any
			reply  string
			closes bool
		}{
			{"disconnect", map[string]any{"type": "disconnect", "message": "interrupted"}, "* BYE interrupted", true},
			{"silent_disconnect", map[string]any{"type": "disconnect", "silent": true}, "", true},
			{"response", map[string]any{"type": "response", "response": "{tag} NO interrupted"}, "a NO interrupted", false},
			{"response_close", map[string]any{"type": "response", "response": "{tag} NO interrupted", "close": true}, "a NO interrupted", true},
		} {
			t.Run(command+"/"+action.name, func(t *testing.T) {
				a := testStore(t, defaultConfig())
				appendTest(t, a, "first@test")
				appendTest(t, a, "second@test")
				appendTest(t, a, "third@test")
				w := imapWire(t, imapAddress(t, a.service), mailboxUsername, mailboxPassword)
				imapReply(t, w, "SELECT Sent", "OK")
				f := rule("interrupt-second", "imap", command, "content", action.fields)
				f["filter"] = map[string]any{"message": map[string]string{"message_id": "second@test"}}
				f["max_hits"] = 1
				installFault(t, a, false, f)
				if err := w.PrintfLine("a %s 1:* FLAGS", command); err != nil {
					t.Fatal(err)
				}
				if line, err := w.ReadLine(); err != nil || !strings.HasPrefix(line, "* 1 FETCH (") {
					t.Fatal("missing first FETCH", line, err)
				}
				if action.reply != "" {
					if line, err := w.ReadLine(); err != nil || line != action.reply {
						t.Fatal("missing fault reply", line, err)
					}
				}
				if action.closes {
					assertClosed(t, w)
				} else {
					imapReply(t, w, "NOOP", "OK")
					response := imapReply(t, w, command+" 1:* FLAGS", "OK")
					if strings.Count(response, " FETCH (") != 3 {
						t.Fatal("connection did not recover", response)
					}
				}
			})
		}
	}
}
