package sandbox

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestSMTPClientConnectDoesNotConsumeGreetingFault(t *testing.T) {
	a := testStore(t, defaultConfig())
	addr := smtpAddress(t, a)
	w := smtpWire(t, addr)
	f := rule("banner", "smtp", "CONNECT", "before", map[string]any{"type": "reject", "code": 421})
	f["max_hits"] = 1
	installFault(t, a, true, f)
	smtpReply(t, w, "CONNECT", 501)
	smtpReply(t, w, "NOOP", 250)
	if got, _ := a.service.serverFaults.get("banner"); got.Hits != 0 {
		t.Fatal("client command consumed greeting rule", got.Hits)
	}
	rejected := faultWire(t, addr, false)
	smtpReply(t, rejected, "", 421)
	assertClosed(t, rejected)
	if got, _ := a.service.serverFaults.get("banner"); got.Hits != 1 {
		t.Fatal("greeting did not claim rule", got.Hits)
	}
	smtpWire(t, addr)
}

func TestIMAPGreetingFaultsRunAfterTLSHandshake(t *testing.T) {
	for _, action := range []string{"delay", "silent", "bye", "response"} {
		t.Run(action, func(t *testing.T) {
			a := testStore(t, defaultConfig())
			params := map[string]any{"type": action}
			switch action {
			case "delay":
				params["delay_ms"] = 1200
			case "silent", "bye":
				params["type"] = "disconnect"
				params["silent"] = action == "silent"
			case "response":
				params["response"] = "* BYE injected greeting"
				params["close"] = true
			}
			f := rule("banner", "imap", "CONNECT", "before", params)
			f["max_hits"] = 1
			installFault(t, a, true, f)
			addr := imapAddress(t, a.service)
			start := time.Now()
			// faultWire verifies TLS and finishes the handshake before returning.
			// Both BYE and silent disconnect must happen after it succeeds.
			w := faultWire(t, addr, true)
			if action == "delay" {
				if time.Since(start) >= time.Second {
					t.Fatal("greeting delay stalled TLS handshake")
				}
				line, err := w.ReadLine()
				if err != nil || !strings.HasPrefix(line, "* OK") {
					t.Fatal(line, err)
				}
				if time.Since(start) < 1100*time.Millisecond {
					t.Fatal("banner was not delayed")
				}
			} else {
				if action != "silent" {
					line, err := w.ReadLine()
					if err != nil || !strings.HasPrefix(line, "* BYE") {
						t.Fatal(line, err)
					}
				}
				assertClosed(t, w)
			}
			imapWire(t, addr, mailboxUsername, mailboxPassword)
		})
	}
}

func TestSequenceCopyUsesViewBeforeExternalExpunge(t *testing.T) {
	a := testStore(t, defaultConfig())
	first := appendTest(t, a, "first@test")
	appendTest(t, a, "second@test")
	appendTest(t, a, "third@test")
	w := imapWire(t, imapAddress(t, a.service), mailboxUsername, mailboxPassword)
	imapReply(t, w, "SELECT Sent", "OK")
	apiCall(t, controlHandler(a), "DELETE", defaultAPI+"/messages/"+first.ID, nil, 204)
	reply := imapReply(t, w, "COPY 2 INBOX", "OK")
	if strings.Count(reply, "* 1 EXPUNGE") != 1 {
		t.Fatal("missing post-copy expunge", reply)
	}
	imapReply(t, w, "SELECT INBOX", "OK")
	reply = imapReply(t, w, "FETCH 1 BODY.PEEK[HEADER.FIELDS (MESSAGE-ID)]", "OK")
	if !strings.Contains(reply, "<second@test>") {
		t.Fatal("COPY used post-expunge sequence numbers", reply)
	}
}

func TestSMTPRejectedBDATCanRetrySameConnection(t *testing.T) {
	a := testStore(t, defaultConfig())
	f := rule("chunk", "smtp", "BDAT", "before", map[string]any{"type": "reject", "code": 451})
	f["max_hits"] = 1
	installFault(t, a, true, f)
	w := smtpWire(t, smtpAddress(t, a))
	raw := testRaw("chunk@test", "retry", "recipient@example.test")
	for attempt := 0; attempt < 2; attempt++ {
		smtpReply(t, w, "MAIL FROM:<sender@example.test>", 250)
		smtpReply(t, w, "RCPT TO:<recipient@example.test>", 250)
		if _, err := fmt.Fprintf(w.W, "BDAT %d LAST\r\n%sNOOP\r\n", len(raw), raw); err != nil {
			t.Fatal(err)
		}
		if err := w.W.Flush(); err != nil {
			t.Fatal(err)
		}
		code := 451
		if attempt == 1 {
			code = 250
		}
		smtpReply(t, w, "", code)
		smtpReply(t, w, "", 250)
		if len(a.snapshot()) != attempt {
			t.Fatal("rejected chunk was stored or retry was lost")
		}
	}
}
