package sandbox

import (
	"encoding/json"
	"fmt"
	"net/textproto"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestFailureSequenceOnSMTPAndIMAPWire(t *testing.T) {
	a := testStore(t, defaultConfig())
	f := rule("third", "smtp", "RCPT", "before", map[string]any{"type": "reject", "code": 450})
	f["sequence"] = map[string]any{"steps": []string{"pass", "pass", "apply"}}
	f["filter"] = map[string]any{"message": map[string]string{"to": "target@example.test"}}
	installFault(t, a, false, f)
	w := smtpWire(t, smtpAddress(t, a))
	smtpReply(t, w, "MAIL FROM:<sender@example.test>", 250)
	for i, code := range []int{250, 250, 450, 250, 250} {
		smtpReply(t, w, fmt.Sprintf("RCPT TO:<other-%d@example.test>", i), 250)
		smtpReply(t, w, "RCPT TO:<target@example.test>", code)
	}
	got, _ := a.faults.get("third")
	if got.Matches != 5 || got.Hits != 1 {
		t.Fatal(got)
	}
	f = rule("repeat", "imap", "NOOP", "before", map[string]any{"type": "reject", "status": "NO"})
	f["sequence"] = map[string]any{"steps": []string{"pass", "pass", "apply"}, "repeat": true}
	installFault(t, a, false, f)
	iw := imapWire(t, imapAddress(t, a.service), mailboxUsername, mailboxPassword)
	for _, status := range []string{"OK", "OK", "NO", "OK", "OK", "NO", "OK"} {
		imapReply(t, iw, "NOOP", status)
	}
	got, _ = a.faults.get("repeat")
	if got.Matches != 7 || got.Hits != 2 {
		t.Fatal(got)
	}
	// Replacing or toggling does not rewind; client-supplied counters are ignored.
	f["matches"], f["hits"] = 0, 0
	apiCall(t, controlHandler(a), "PUT", defaultAPI+"/faults/repeat", f, 200)
	imapReply(t, iw, "NOOP", "OK")
	imapReply(t, iw, "NOOP", "NO")
}

func TestFailureSequencesClaimAtomically(t *testing.T) {
	a := testStore(t, defaultConfig())
	f := rule("third", "imap", "NOOP", "before", map[string]any{"type": "reject", "status": "NO"})
	f["sequence"] = map[string]any{"steps": []string{"pass", "pass", "apply"}, "repeat": true}
	installFault(t, a, false, f)
	var hits atomic.Int32
	var wg sync.WaitGroup
	for range 300 {
		wg.Go(func() {
			hits.Add(int32(len(a.faults.claim(protocolEvent{protocol: "imap", command: "NOOP", phase: "before"}, false))))
		})
	}
	wg.Wait()
	got, _ := a.faults.get("third")
	if hits.Load() != 100 || got.Hits != 100 || got.Matches != 300 {
		t.Fatal(hits.Load(), got)
	}
}

func startIdle(t *testing.T, w *textproto.Conn) {
	t.Helper()
	if err := w.PrintfLine("i IDLE"); err != nil {
		t.Fatal(err)
	}
	if line, err := w.ReadLine(); err != nil || !strings.HasPrefix(line, "+") {
		t.Fatal(line, err)
	}
}
func finishIdle(t *testing.T, w *textproto.Conn) {
	t.Helper()
	if err := w.PrintfLine("DONE"); err != nil {
		t.Fatal(err)
	}
	if line, err := w.ReadLine(); err != nil || !strings.HasPrefix(line, "i OK ") {
		t.Fatal("unexpected IDLE notification/completion", line, err)
	}
}
func TestActiveIdleDisconnectAndCancellation(t *testing.T) {
	for _, silent := range []bool{false, true} {
		t.Run(fmt.Sprint(silent), func(t *testing.T) {
			a := testStore(t, defaultConfig())
			f := rule("timeout", "imap", "IDLE", "active", map[string]any{"type": "disconnect", "after_ms": 100, "silent": silent, "message": "Renew IDLE"})
			f["max_hits"] = 1
			installFault(t, a, false, f)
			addr := imapAddress(t, a.service)
			w := imapWire(t, addr, mailboxUsername, mailboxPassword)
			imapReply(t, w, "SELECT INBOX", "OK")
			start := time.Now()
			startIdle(t, w)
			if !silent {
				if line, err := w.ReadLine(); err != nil || line != "* BYE Renew IDLE" {
					t.Fatal(line, err)
				}
			}
			assertClosed(t, w)
			if time.Since(start) < 75*time.Millisecond {
				t.Fatal("IDLE closed before interval")
			}
			// The one-shot timeout doesn't affect a renewed connection.
			w = imapWire(t, addr, mailboxUsername, mailboxPassword)
			imapReply(t, w, "SELECT INBOX", "OK")
			startIdle(t, w)
			finishIdle(t, w)
			imapReply(t, w, "NOOP", "OK")
		})
	}
	t.Run("DONE cancels timer", func(t *testing.T) {
		a := testStore(t, defaultConfig())
		f := rule("timeout", "imap", "IDLE", "active", map[string]any{"type": "disconnect", "after_ms": 120})
		f["max_hits"] = 1
		installFault(t, a, false, f)
		w := imapWire(t, imapAddress(t, a.service), mailboxUsername, mailboxPassword)
		imapReply(t, w, "SELECT INBOX", "OK")
		startIdle(t, w)
		awaitFault(t, a.faults, "timeout", 1)
		finishIdle(t, w)
		time.Sleep(160 * time.Millisecond)
		imapReply(t, w, "NOOP", "OK")
	})
}

func TestIdleNotificationDropsAreScopedAndRecover(t *testing.T) {
	for _, kind := range []string{"EXISTS", "FLAGS", "EXPUNGE"} {
		t.Run(kind, func(t *testing.T) {
			service, h := testManager(t, defaultConfig())
			one := createAccount(t, h, "one")
			two := createAccount(t, h, "two")
			a := service.lookup(one.ID)
			msg := appendTest(t, a, "original@test")
			f := rule("drop", "imap", "IDLE", "notification", map[string]any{"type": "drop"})
			f["filter"] = map[string]string{"notification": kind, "folder": "Sent"}
			f["max_hits"] = 1
			installFault(t, a, false, f)
			addr := imapAddress(t, service)
			w := imapWire(t, addr, one.Username, one.Password)
			imapReply(t, w, "SELECT Sent", "OK")
			startIdle(t, w)
			switch kind {
			case "EXISTS":
				appendTest(t, a, "new@test")
			case "FLAGS":
				apiCall(t, h, "PATCH", one.APIBase+"/messages/"+msg.ID, map[string]any{"flags": []string{`\Seen`}}, 200)
			case "EXPUNGE":
				apiCall(t, h, "DELETE", one.APIBase+"/messages/"+msg.ID, nil, 204)
			}
			awaitFault(t, a.faults, "drop", 1)
			finishIdle(t, w)
			imapReply(t, w, "SELECT Sent", "OK") // resynchronizes after the deliberately lost update
			startIdle(t, w)
			appendTest(t, a, "recovery@test")
			if line, err := w.ReadLine(); err != nil || !strings.HasSuffix(line, " EXISTS") {
				t.Fatal(line, err)
			}
			finishIdle(t, w)
			other := imapWire(t, addr, two.Username, two.Password)
			imapReply(t, other, "SELECT Sent", "OK")
			startIdle(t, other)
			appendTest(t, service.lookup(two.ID), "unaffected@test")
			if line, err := other.ReadLine(); err != nil || line != "* 1 EXISTS" {
				t.Fatal(line, err)
			}
			finishIdle(t, other)
		})
	}
}

func TestSequenceAndIdleValidation(t *testing.T) {
	a := testStore(t, defaultConfig())
	for _, seq := range []any{map[string]any{"steps": []string{}}, map[string]any{"steps": []string{"fail"}}, map[string]any{"steps": []string{"pass"}, "unknown": true}} {
		f := rule("bad", "smtp", "NOOP", "before", map[string]any{"type": "reject", "code": 451})
		f["sequence"] = seq
		apiCall(t, controlHandler(a), "POST", defaultAPI+"/faults", f, 400)
	}
	f := rule("bad", "smtp", "NOOP", "before", map[string]any{"type": "reject", "code": 451})
	f["sequence"] = map[string]any{"steps": []string{"apply"}}
	f["probability"] = 1
	apiCall(t, controlHandler(a), "POST", defaultAPI+"/faults", f, 400)
	for _, definition := range []map[string]any{
		rule("bad", "smtp", "NOOP", "active", map[string]any{"type": "disconnect"}),
		rule("bad", "imap", "IDLE", "active", map[string]any{"type": "reject", "status": "NO"}),
		rule("bad", "imap", "IDLE", "notification", map[string]any{"type": "disconnect"}),
		rule("bad", "imap", "IDLE", "before", map[string]any{"type": "drop"}),
		rule("bad", "imap", "NOOP", "before", map[string]any{"type": "disconnect", "after_ms": 0}),
		rule("bad", "imap", "IDLE", "active", map[string]any{"type": "disconnect", "after_ms": -1}),
	} {
		apiCall(t, controlHandler(a), "POST", defaultAPI+"/faults", definition, 400)
	}
	var rules []faultRule
	json.Unmarshal(apiCall(t, controlHandler(a), "GET", defaultAPI+"/faults", nil, 200).Body.Bytes(), &rules)
	if len(rules) != 0 {
		t.Fatal("invalid faults installed")
	}
}
