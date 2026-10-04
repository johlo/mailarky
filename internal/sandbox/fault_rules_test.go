package sandbox

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/smtp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func rule(name, protocol, command, phase string, action map[string]any) map[string]any {
	return map[string]any{"name": name, "trigger": map[string]string{"protocol": protocol, "command": command, "phase": phase}, "action": action}
}
func installFault(t *testing.T, a *Account, server bool, definition map[string]any) {
	t.Helper()
	path := a.service.config.Webroot + "/api/v1/accounts/" + a.identity.ID + "/faults"
	if server {
		path = a.service.config.Webroot + "/api/v1/faults"
	}
	apiCall(t, controlHandler(a), "POST", path, definition, 201)
}
func awaitFault(t *testing.T, registry *faultRegistry, name string, hits uint64) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if f, ok := registry.get(name); ok && f.Hits >= hits {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("fault did not fire", name)
}
func TestRecipientFiltersUseOnlyCurrentRecipient(t *testing.T) {
	for _, phase := range []string{"before", "after"} {
		t.Run(phase, func(t *testing.T) {
			a := testStore(t, defaultConfig())
			f := rule("rcpt", "smtp", "RCPT", phase, map[string]any{"type": "delay", "delay_ms": 1})
			f["filter"] = map[string]any{"message": map[string]string{"to": "x@example.test"}}
			installFault(t, a, false, f)
			c, err := smtp.Dial(smtpAddress(t, a))
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			if err := c.Mail("sender@example.test"); err != nil {
				t.Fatal(err)
			}
			for _, recipient := range []string{"x@example.test", "y@example.test", "z@example.test"} {
				if err := c.Rcpt(recipient); err != nil {
					t.Fatal(err)
				}
			}
			got, _ := a.faults.get("rcpt")
			if got.Hits != 1 {
				t.Fatalf("RCPT fault fired %d times", got.Hits)
			}
		})
	}
}
func TestMAILFaultRunsAtMAILOnlyForKnownAccount(t *testing.T) {
	cfg := defaultConfig()
	cfg.SMTPAllowInsecureAuth = true
	a := testStore(t, cfg)
	f := rule("sender", "smtp", "MAIL", "before", map[string]any{"type": "reject", "code": 451})
	f["filter"] = map[string]any{"message": map[string]string{"from": "sender@example.test"}}
	installFault(t, a, false, f)
	addr := smtpAddress(t, a)
	anonymous := smtpWire(t, addr)
	smtpReply(t, anonymous, "MAIL FROM:<sender@example.test>", 250)
	smtpReply(t, anonymous, "RCPT TO:<recipient@example.test>", 250)
	authenticated := smtpWire(t, addr)
	smtpReply(t, authenticated, "AUTH PLAIN "+base64.StdEncoding.EncodeToString([]byte("\x00"+mailboxUsername+"\x00"+mailboxPassword)), 235)
	smtpReply(t, authenticated, "MAIL FROM:<sender@example.test>", 451)
	got, _ := a.faults.get("sender")
	if got.Hits != 1 {
		t.Fatal(got.Hits)
	}
}
func TestAccountFaultsAndServerRegistryAreSeparate(t *testing.T) {
	service, h := testManager(t, defaultConfig())
	one := createAccount(t, h, "one")
	two := createAccount(t, h, "two")
	a, b := service.lookup(one.ID), service.lookup(two.ID)
	f := rule("reject", "smtp", "DATA", "content", map[string]any{"type": "reject", "code": 451})
	f["filter"] = map[string]any{"message": map[string]any{"headers": map[string]string{"X-Test-ID": "same"}}}
	installFault(t, a, false, f)
	root := service.lookup(defaultAccountID)
	installFault(t, root, true, rule("server", "imap", "NOOP", "before", map[string]any{"type": "reject", "status": "NO"}))
	var accountRules, serverRules []faultRule
	json.Unmarshal(apiCall(t, h, "GET", defaultAPI+"/faults", nil, 200).Body.Bytes(), &accountRules)
	json.Unmarshal(apiCall(t, h, "GET", "/api/v1/faults", nil, 200).Body.Bytes(), &serverRules)
	if len(accountRules) != 0 || len(serverRules) != 1 {
		t.Fatal("server and account registries mixed")
	}
	addr := smtpAddress(t, root)
	for _, target := range []*Account{a, b} {
		err := smtp.SendMail(addr, nil, "sender@example.test", target.identity.Recipients, testRaw("same@test", "same", "customer@example.test"))
		if (target == a) != (err != nil) {
			t.Fatal("fault crossed account", target.identity.ID, err)
		}
	}
	if len(a.snapshot()) != 0 || len(b.snapshot()) != 1 {
		t.Fatal("delivery crossed accounts")
	}
	apiCall(t, h, "PATCH", one.APIBase+"/faults/reject", map[string]bool{"enabled": false}, 200)
	if err := smtp.SendMail(addr, nil, "sender@example.test", one.Recipients, testRaw("retry@test", "same", "customer@example.test")); err != nil {
		t.Fatal(err)
	}
	apiCall(t, h, "DELETE", one.APIBase+"/faults/reject", nil, 204)
	apiCall(t, h, "GET", one.APIBase+"/faults/reject", nil, 404)
}
func TestDelaysDoNotHoldStoreOrRegistryLocks(t *testing.T) {
	a := testStore(t, defaultConfig())
	f := rule("slow", "smtp", "DATA", "content", map[string]any{"type": "delay", "delay_ms": 250})
	f["filter"] = map[string]any{"message": map[string]string{"message_id": "slow@test"}}
	installFault(t, a, false, f)
	addr := smtpAddress(t, a)
	slow := make(chan error, 1)
	go func() {
		slow <- smtp.SendMail(addr, nil, "sender@example.test", []string{"recipient@example.test"}, testRaw("slow@test", "slow", "recipient@example.test"))
	}()
	awaitFault(t, a.faults, "slow", 1)
	fast := make(chan error, 1)
	go func() {
		fast <- smtp.SendMail(addr, nil, "sender@example.test", []string{"recipient@example.test"}, testRaw("fast@test", "fast", "recipient@example.test"))
	}()
	select {
	case err := <-fast:
		if err != nil {
			t.Fatal(err)
		}
	case <-slow:
		t.Fatal("independent transaction waited for delay")
	}
	if err := <-slow; err != nil {
		t.Fatal(err)
	}
}
func TestFaultClaimsAreAtomicAndUpdatesPreserveHits(t *testing.T) {
	a := testStore(t, defaultConfig())
	f := rule("bounded", "imap", "SEARCH", "content", map[string]any{"type": "hide"})
	f["filter"] = map[string]any{"message": map[string]string{"message_id": "target@test"}}
	f["max_hits"] = 7
	installFault(t, a, false, f)
	m := appendTest(t, a, "target@test")
	var wg sync.WaitGroup
	var total atomic.Int32
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			total.Add(int32(len(a.faults.claim(protocolEvent{protocol: "imap", command: "SEARCH", phase: "content", message: m}, false))))
		}()
	}
	wg.Wait()
	if total.Load() != 7 {
		t.Fatal(total.Load())
	}
	f["hits"] = 999
	apiCall(t, controlHandler(a), "PUT", defaultAPI+"/faults/bounded", f, 200)
	got, _ := a.faults.get("bounded")
	if got.Hits != 7 {
		t.Fatal("counter rewritten", got.Hits)
	}
	zero := 0.0
	f["name"] = "never"
	f["probability"] = zero
	installFault(t, a, false, f)
	if rules := a.faults.claim(protocolEvent{protocol: "imap", command: "SEARCH", phase: "content", message: m}, false); len(rules) != 0 {
		t.Fatal("zero probability fired")
	}
}
func TestStrictFaultActionAndTriggerValidation(t *testing.T) {
	a := testStore(t, defaultConfig())
	base := rule("bad", "smtp", "DATA", "content", map[string]any{"type": "reject", "code": 451})
	for _, action := range []map[string]any{{"type": "delay"}, {"type": "replace_body"}, {"type": "delay", "delay_ms": nil}, {"type": "reject", "code": 200}, {"type": "reject", "code": 451, "delay_ms": 2}, {"type": "delay", "delay_ms": -1}, {"type": "delay", "delay_ms": 30001}, {"type": "disconnect", "message": "bad\r\nreply"}, {"type": "response", "response": ""}, {"type": "unknown"}, {"type": "reject", "code": 451, "enhanced_code": "5.1.1"}, {"type": "reject", "code": 451, "status": "NO"}} {
		base["action"] = action
		apiCall(t, controlHandler(a), "POST", defaultAPI+"/faults", base, 400)
	}
	for _, trigger := range []map[string]string{{"protocol": "smtp", "command": "data", "phase": "before"}, {"protocol": "smtp", "command": "DATA", "phase": "sender"}, {"protocol": "imap", "command": "NOOP", "phase": "content"}} {
		base["trigger"] = trigger
		base["action"] = map[string]any{"type": "delay", "delay_ms": 1}
		apiCall(t, controlHandler(a), "POST", defaultAPI+"/faults", base, 400)
	}
	f := rule("early", "smtp", "MAIL", "before", map[string]any{"type": "delay", "delay_ms": 1})
	f["filter"] = map[string]any{"message": map[string]string{"message_id": "unknown@test"}}
	apiCall(t, controlHandler(a), "POST", defaultAPI+"/faults", f, 400)
}
func TestSMTPRejectGreetingAUTHSTARTTLSAndCapabilities(t *testing.T) {
	t.Run("greeting", func(t *testing.T) {
		a := testStore(t, defaultConfig())
		installFault(t, a, true, rule("greeting", "smtp", "CONNECT", "before", map[string]any{"type": "reject", "code": 421}))
		w := faultWire(t, smtpAddress(t, a), false)
		smtpReply(t, w, "", 421)
		assertClosed(t, w)
	})
	t.Run("auth", func(t *testing.T) {
		cfg := defaultConfig()
		cfg.SMTPAllowInsecureAuth = true
		a := testStore(t, cfg)
		installFault(t, a, false, rule("auth", "smtp", "AUTH", "before", map[string]any{"type": "reject", "code": 454, "enhanced_code": "4.7.0"}))
		w := smtpWire(t, smtpAddress(t, a))
		smtpReply(t, w, "AUTH PLAIN "+base64.StdEncoding.EncodeToString([]byte("\x00"+mailboxUsername+"\x00"+mailboxPassword)), 454)
	})
	t.Run("starttls", func(t *testing.T) {
		a := testStore(t, defaultConfig())
		installFault(t, a, true, rule("tls", "smtp", "STARTTLS", "before", map[string]any{"type": "reject", "code": 454}))
		w := smtpWire(t, smtpAddress(t, a))
		smtpReply(t, w, "STARTTLS", 454)
	})
	t.Run("capabilities", func(t *testing.T) {
		a := testStore(t, defaultConfig())
		installFault(t, a, true, rule("caps", "smtp", "EHLO", "after", map[string]any{"type": "capabilities", "omit": []string{"CHUNKING", "SMTPUTF8"}}))
		w := faultWire(t, smtpAddress(t, a), false)
		smtpReply(t, w, "", 220)
		if reply := smtpReply(t, w, "EHLO localhost", 250); strings.Contains(reply, "CHUNKING") || strings.Contains(reply, "SMTPUTF8") {
			t.Fatal(reply)
		}
	})
}
func TestSMTPAfterStoreFaultLeavesMessageForRetryAssertions(t *testing.T) {
	for _, command := range []string{"DATA", "BDAT"} {
		t.Run(command, func(t *testing.T) {
			a := testStore(t, defaultConfig())
			f := rule("lost-ack", "smtp", command, "after", map[string]any{"type": "disconnect", "silent": true})
			f["filter"] = map[string]any{"message": map[string]string{"message_id": "stored@test"}}
			f["max_hits"] = 1
			installFault(t, a, false, f)
			w := smtpWire(t, smtpAddress(t, a))
			smtpReply(t, w, "MAIL FROM:<sender@example.test>", 250)
			smtpReply(t, w, "RCPT TO:<recipient@example.test>", 250)
			raw := testRaw("stored@test", "lost", "recipient@example.test")
			if command == "DATA" {
				smtpReply(t, w, "DATA", 354)
				out := w.DotWriter()
				out.Write(raw)
				out.Close()
			} else {
				half := len(raw) / 2
				w.PrintfLine("BDAT %d", half)
				w.W.Write(raw[:half])
				w.W.Flush()
				smtpReply(t, w, "", 250)
				w.PrintfLine("BDAT %d LAST", len(raw)-half)
				w.W.Write(raw[half:])
				w.W.Flush()
			}
			assertClosed(t, w)
			if len(a.snapshot()) != 1 {
				t.Fatal("message wasn't stored before disconnect")
			}
			got, _ := a.faults.get("lost-ack")
			if got.Hits != 1 {
				t.Fatal(got.Hits)
			}
		})
	}
}
func TestSMTPContentFaultRejectsFanoutBeforeAnyStore(t *testing.T) {
	service, h := testManager(t, defaultConfig())
	one := createAccount(t, h, "one")
	two := createAccount(t, h, "two")
	installFault(t, service.lookup(two.ID), false, rule("reject", "smtp", "DATA", "content", map[string]any{"type": "reject", "code": 451}))
	err := smtp.SendMail(smtpAddress(t, service.lookup(defaultAccountID)), nil, "sender@example.test", []string{one.Recipients[0], two.Recipients[0]}, testRaw("fanout@test", "fanout", one.Recipients[0]))
	if err == nil {
		t.Fatal("expected transaction rejection")
	}
	if accountCount(t, h, one.APIBase) != 0 || accountCount(t, h, two.APIBase) != 0 {
		t.Fatal("content fault partially stored fanout")
	}
}
func TestIMAPFaultsCoverLoginCommandsAndMessageContent(t *testing.T) {
	a := testStore(t, defaultConfig())
	m := appendTest(t, a, "one@test")
	other := appendTest(t, a, "two@test")
	addr := imapAddress(t, a.service)
	auth := rule("auth", "imap", "LOGIN", "before", map[string]any{"type": "reject", "status": "NO", "response_code": "UNAVAILABLE"})
	auth["max_hits"] = 1
	installFault(t, a, false, auth)
	w := imapWire(t, addr, "", "")
	imapReply(t, w, fmt.Sprintf("LOGIN %q %q", mailboxUsername, mailboxPassword), "NO")
	imapReply(t, w, fmt.Sprintf("LOGIN %q %q", mailboxUsername, mailboxPassword), "OK")
	selectFault := rule("select", "imap", "SELECT", "before", map[string]any{"type": "reject", "status": "NO"})
	selectFault["filter"] = map[string]string{"folder": "Sent"}
	selectFault["max_hits"] = 1
	installFault(t, a, false, selectFault)
	imapReply(t, w, "SELECT Sent", "NO")
	imapReply(t, w, "SELECT Sent", "OK")
	hide := rule("hide", "imap", "UID SEARCH", "content", map[string]any{"type": "hide"})
	hide["filter"] = map[string]any{"message": map[string]string{"message_id": "one@test"}}
	installFault(t, a, false, hide)
	if reply := imapReply(t, w, "UID SEARCH ALL", "OK"); !strings.Contains(reply, fmt.Sprintf("* SEARCH %d", other.UID)) {
		t.Fatal(reply)
	}
	replace := rule("replace", "imap", "UID FETCH", "content", map[string]any{"type": "replace_header", "header": "Subject", "value": "changed"})
	replace["filter"] = map[string]any{"message": map[string]string{"message_id": "one@test"}}
	installFault(t, a, false, replace)
	if reply := imapReply(t, w, "UID FETCH 1:* ENVELOPE", "OK"); !strings.Contains(reply, "changed") {
		t.Fatal(reply)
	}
	if strings.Contains(string(a.get(m.ID).Raw), "changed") {
		t.Fatal("fault mutated stored MIME")
	}
	reset := rule("reset", "imap", "SELECT", "before", map[string]any{"type": "reset_uidvalidity"})
	reset["filter"] = map[string]string{"folder": "Sent"}
	reset["max_hits"] = 1
	installFault(t, a, false, reset)
	otherConn := imapWire(t, addr, mailboxUsername, mailboxPassword)
	imapReply(t, otherConn, "SELECT Sent", "OK")
	w.PrintfLine("a NOOP")
	if line, err := w.ReadLine(); err != nil || !strings.HasPrefix(line, "* BYE") {
		t.Fatal(line, err)
	}
	assertClosed(t, w)
}
