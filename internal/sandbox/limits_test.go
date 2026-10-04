package sandbox

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/smtp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestSMTPRecipientLimitAndTransactionReset(t *testing.T) {
	cfg := defaultConfig()
	cfg.SMTPMaxRecipients = 2
	a := testStore(t, cfg)
	w := smtpWire(t, smtpAddress(t, a))
	if reply := smtpReply(t, w, "EHLO localhost", 250); !strings.Contains(reply, "LIMITS RCPTMAX=2") {
		t.Fatal(reply)
	}
	for range 2 {
		smtpReply(t, w, "MAIL FROM:<sender@example.test>", 250)
		for i, code := range []int{250, 250, 452} {
			smtpReply(t, w, fmt.Sprintf("RCPT TO:<r%d@example.test>", i), code)
		}
		smtpReply(t, w, "DATA", 354)
		out := w.DotWriter()
		out.Write(testRaw("batch@test", "batch", "r0@example.test"))
		out.Close()
		smtpReply(t, w, "", 250)
	}
	if len(a.snapshot()) != 2 {
		t.Fatal("accepted recipients were lost")
	}
}

func waitResource(t *testing.T, a *Account, protocol string, want int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		a.resourceMu.Lock()
		got := a.smtpConnections
		if protocol == "imap" {
			got = a.imapConnections
		}
		a.resourceMu.Unlock()
		if got == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("connection count didn't settle", protocol, want)
}
func TestAccountConnectionLimitsReleaseAndIsolate(t *testing.T) {
	cfg := defaultConfig()
	cfg.SMTPAllowInsecureAuth = true
	cfg.AccountLimits = accountLimits{SMTPConnections: 1, IMAPConnections: 1}
	service, h := testManager(t, cfg)
	one := createAccount(t, h, "one")
	two := createAccount(t, h, "two")
	a := service.lookup(one.ID)
	addr := smtpAddress(t, a)
	auth := func(user, pass string) string {
		return "AUTH PLAIN " + base64.StdEncoding.EncodeToString([]byte("\x00"+user+"\x00"+pass))
	}
	first, second := smtpWire(t, addr), smtpWire(t, addr)
	smtpReply(t, first, auth(one.Username, one.Password), 235)
	smtpReply(t, second, auth(one.Username, "wrong"), 535)
	smtpReply(t, second, auth(one.Username, one.Password), 454)
	other := smtpWire(t, addr)
	smtpReply(t, other, auth(two.Username, two.Password), 235)
	smtpReply(t, first, "QUIT", 221)
	waitResource(t, a, "smtp", 0)
	smtpReply(t, second, auth(one.Username, one.Password), 235)
	second.Close()
	waitResource(t, a, "smtp", 0)
	addr = imapAddress(t, service)
	i1 := imapWire(t, addr, one.Username, one.Password)
	i2 := imapWire(t, addr, "", "")
	if reply := imapReply(t, i2, fmt.Sprintf("LOGIN %q %q", one.Username, one.Password), "NO"); !strings.Contains(reply, "[LIMIT]") {
		t.Fatal(reply)
	}
	imapWire(t, addr, two.Username, two.Password)
	imapReply(t, i1, "LOGOUT", "OK")
	waitResource(t, a, "imap", 0)
	imapReply(t, i2, fmt.Sprintf("LOGIN %q %q", one.Username, one.Password), "OK")
	i2.Close()
	waitResource(t, a, "imap", 0)
}

func TestSMTPRateLimitAppliesOnlyToAuthenticatedSender(t *testing.T) {
	cfg := defaultConfig()
	cfg.SMTPAllowInsecureAuth = true
	cfg.AccountLimits = accountLimits{SendMessages: 1, SendWindow: "250ms"}
	service, h := testManager(t, cfg)
	one := createAccount(t, h, "one")
	two := createAccount(t, h, "two")
	a, b := service.lookup(one.ID), service.lookup(two.ID)
	addr := smtpAddress(t, a)
	send := func(account *mailboxDescription, to ...string) error {
		var auth smtp.Auth
		if account != nil {
			auth = smtp.PlainAuth("", account.Username, account.Password, "127.0.0.1")
		}
		return smtp.SendMail(addr, auth, "from@example.test", to, testRaw("rate@test", "rate", "recipient@example.test"))
	}
	if err := send(&one, "customer@example.test"); err != nil {
		t.Fatal(err)
	}
	if err := send(&one, "customer@example.test"); err == nil || !strings.Contains(err.Error(), "4.7.0") {
		t.Fatal("second submission in window was admitted", err)
	}
	if err := send(&two, "customer@example.test"); err != nil {
		t.Fatal("rate limit leaked across accounts", err)
	}
	// Anonymous mail into a limited account is receiving, not sending.
	for range 3 {
		if err := send(nil, one.Recipients[0], two.Recipients[0]); err != nil {
			t.Fatal("anonymous fan-out consumed a sending limit", err)
		}
	}
	time.Sleep(280 * time.Millisecond)
	if err := send(&one, "customer@example.test"); err != nil {
		t.Fatal("window didn't recover", err)
	}
	if len(a.snapshot()) != 5 || len(b.snapshot()) != 4 {
		t.Fatal(len(a.snapshot()), len(b.snapshot()))
	}
}

func TestSMTPRateLimitReturnsReservationWhenStorageFails(t *testing.T) {
	cfg := defaultConfig()
	cfg.SMTPAllowInsecureAuth = true
	cfg.AccountLimits = accountLimits{SendMessages: 1, SendWindow: "1h"}
	service, h := testManager(t, cfg)
	one := createAccount(t, h, "one")
	a := service.lookup(one.ID)
	addr := smtpAddress(t, a)
	auth := smtp.PlainAuth("", one.Username, one.Password, "127.0.0.1")
	send := func() error {
		return smtp.SendMail(addr, auth, "from@example.test", []string{"customer@example.test"}, testRaw("store@test", "store", "customer@example.test"))
	}
	// Without the SMTP folder, the submission passes preflight and fails at append.
	var sent folderState
	if err := a.mutate(func(s *storeState) error { sent = s.Folders["Sent"]; delete(s.Folders, "Sent"); return nil }); err != nil {
		t.Fatal(err)
	}
	if err := send(); err == nil || !strings.Contains(err.Error(), "451") {
		t.Fatal("expected storage failure", err)
	}
	if err := a.mutate(func(s *storeState) error { s.Folders["Sent"] = sent; return nil }); err != nil {
		t.Fatal(err)
	}
	if err := send(); err != nil {
		t.Fatal("failed submission consumed the sending window", err)
	}
}

func TestRateAndQuotaLimitsAreAtomic(t *testing.T) {
	cfg := defaultConfig()
	cfg.AccountLimits = accountLimits{SendMessages: 3, SendWindow: "1h", QuotaMessages: 3}
	a := testStore(t, cfg)
	var admitted, stored atomic.Int32
	var wg sync.WaitGroup
	for range 30 {
		wg.Go(func() {
			if _, ok := a.reserveSend(time.Now()); ok {
				admitted.Add(1)
			}
			if _, err := a.append(testRaw("race@test", "race", "to@example.test"), appendOptions{Folder: "Sent"}); err == nil {
				stored.Add(1)
			}
		})
	}
	wg.Wait()
	if admitted.Load() != 3 || stored.Load() != 3 || len(a.snapshot()) != 3 {
		t.Fatal(admitted.Load(), stored.Load(), len(a.snapshot()))
	}
}

func TestQuotaEnforcedAcrossSMTPHTTPAndIMAP(t *testing.T) {
	cfg := defaultConfig()
	cfg.AccountLimits = accountLimits{QuotaMessages: 1}
	cfg.MaxMessages = 1
	a := testStore(t, cfg)
	h := controlHandler(a)
	msg := appendTest(t, a, "original@test")
	addr := smtpAddress(t, a)
	err := smtp.SendMail(addr, nil, "from@example.test", []string{"to@example.test"}, testRaw("over@test", "over", "to@example.test"))
	if err == nil || !strings.Contains(err.Error(), "4.2.2") {
		t.Fatal(err)
	}
	apiCall(t, h, "POST", defaultAPI+"/messages", map[string]any{"from": "from@example.test", "to": []string{"to@example.test"}, "body": "Over quota"}, 507)
	w := imapWire(t, imapAddress(t, a.service), mailboxUsername, mailboxPassword)
	raw := testRaw("over@test", "over", "to@example.test")
	if reply := imapReply(t, w, fmt.Sprintf("APPEND INBOX {%d+}\r\n%s", len(raw), raw), "NO"); !strings.Contains(reply, "[OVERQUOTA]") {
		t.Fatal(reply)
	}
	imapReply(t, w, "SELECT Sent", "OK")
	if reply := imapReply(t, w, "COPY 1 INBOX", "NO"); !strings.Contains(reply, "[OVERQUOTA]") {
		t.Fatal(reply)
	}
	if len(a.snapshot()) != 1 || a.get(msg.ID) == nil {
		t.Fatal("quota evicted existing fixture")
	}
	apiCall(t, h, "DELETE", defaultAPI+"/messages/"+msg.ID, nil, 204)
	if err := smtp.SendMail(addr, nil, "from@example.test", []string{"to@example.test"}, testRaw("ok@test", "ok", "to@example.test")); err != nil {
		t.Fatal(err)
	}
}

func TestQuotaBytesAndCopyAreAtomic(t *testing.T) {
	for _, useBytes := range []bool{false, true} {
		t.Run(fmt.Sprint(useBytes), func(t *testing.T) {
			cfg := defaultConfig()
			raw := testRaw("copy@test", "copy", "recipient@example.test")
			if useBytes {
				cfg.AccountLimits.QuotaBytes = int64(len(raw) * 3)
			} else {
				cfg.AccountLimits.QuotaMessages = 3
			}
			a := testStore(t, cfg)
			for range 2 {
				if _, err := a.append(raw, appendOptions{Folder: "Sent"}); err != nil {
					t.Fatal(err)
				}
			}
			w := imapWire(t, imapAddress(t, a.service), mailboxUsername, mailboxPassword)
			imapReply(t, w, "SELECT Sent", "OK")
			if reply := imapReply(t, w, "COPY 1:* INBOX", "NO"); !strings.Contains(reply, "[OVERQUOTA]") {
				t.Fatal(reply)
			}
			if len(a.snapshot()) != 2 {
				t.Fatal("COPY partially committed")
			}
			imapReply(t, w, "COPY 1 INBOX", "OK")
			if len(a.snapshot()) != 3 {
				t.Fatal("COPY below quota failed")
			}
		})
	}
}

func TestLimitConfigurationAndAccountOverrides(t *testing.T) {
	t.Setenv("MAILARKY_SMTP_MAX_RECIPIENTS", "2")
	t.Setenv("MAILARKY_ACCOUNT_SEND_MESSAGES", "5")
	t.Setenv("MAILARKY_ACCOUNT_SEND_WINDOW", "1s")
	t.Setenv("MAILARKY_ACCOUNT_QUOTA_BYTES", "4096")
	cfg, err := loadConfig([]string{"--smtp-max-recipients=3"}, io.Discard)
	if err != nil || cfg.SMTPMaxRecipients != 3 || cfg.AccountLimits.SendMessages != 5 || cfg.AccountLimits.QuotaBytes != 4096 {
		t.Fatal(cfg, err)
	}
	_, h := testManager(t, cfg)
	var desc mailboxDescription
	json.Unmarshal(apiCall(t, h, "POST", "/api/v1/accounts", map[string]any{"limits": map[string]any{}}, 201).Body.Bytes(), &desc)
	if desc.Limits != (accountLimits{}) {
		t.Fatal("explicit unlimited limits didn't override defaults", desc.Limits)
	}
	for _, limits := range []map[string]any{{"send_messages": 1}, {"send_window": "0s"}, {"smtp_connections": -1}, {"quota_bytes": -1}} {
		apiCall(t, h, "POST", "/api/v1/accounts", map[string]any{"limits": limits}, 400)
	}
}
