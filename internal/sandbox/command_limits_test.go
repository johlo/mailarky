package sandbox

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/textproto"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func authenticatedSMTPWire(t *testing.T, addr, username, password string) *textproto.Conn {
	t.Helper()
	w := smtpWire(t, addr)
	auth := base64.StdEncoding.EncodeToString([]byte("\x00" + username + "\x00" + password))
	smtpReply(t, w, "AUTH PLAIN "+auth, 235)
	return w
}

func TestCommandRateLimitsShareAccountWindows(t *testing.T) {
	cfg := defaultConfig()
	cfg.SMTPAllowInsecureAuth = true
	cfg.AccountLimits = accountLimits{SMTPCommands: 2, SMTPCommandWindow: "1h", IMAPCommands: 2, IMAPCommandWindow: "1h"}
	service, h := testManager(t, cfg)
	one, two := createAccount(t, h, "one"), createAccount(t, h, "two")
	a := service.lookup(one.ID)
	smtpAddr := smtpAddress(t, a)
	first := authenticatedSMTPWire(t, smtpAddr, one.Username, one.Password)
	second := authenticatedSMTPWire(t, smtpAddr, one.Username, one.Password)
	smtpReply(t, first, "MAIL FROM:<sender@example.test>", 250)
	smtpReply(t, second, "NOOP", 250)
	if reply := smtpReply(t, first, "RCPT TO:<recipient@example.test>", 451); !strings.Contains(reply, "4.7.0") {
		t.Fatal(reply)
	}
	smtpReply(t, second, "NOOP", 451)
	smtpReply(t, first, "RSET", 250)
	smtpReply(t, first, "QUIT", 221)
	assertClosed(t, first)
	reconnected := authenticatedSMTPWire(t, smtpAddr, one.Username, one.Password)
	smtpReply(t, reconnected, "NOOP", 451)
	other := authenticatedSMTPWire(t, smtpAddr, two.Username, two.Password)
	smtpReply(t, other, "NOOP", 250)

	// HTTP setup and reads work while SMTP is exhausted and do not consume IMAP.
	for range 3 {
		apiCall(t, h, "POST", one.APIBase+"/messages", map[string]any{"from": "sender@example.test", "to": []string{"recipient@example.test"}, "body": "fixture"}, 201)
		apiCall(t, h, "GET", one.APIBase+"/messages", nil, 200)
	}
	imapAddr := imapAddress(t, service)
	i1 := imapWire(t, imapAddr, one.Username, one.Password)
	i2 := imapWire(t, imapAddr, one.Username, one.Password)
	imapReply(t, i1, "SELECT INBOX", "OK")
	imapReply(t, i2, "NOOP", "OK")
	for _, command := range []string{"UID FETCH 1 (BODY[])", "CREATE Blocked", "NOOP"} {
		if reply := imapReply(t, i1, command, "NO"); !strings.Contains(reply, "[LIMIT]") {
			t.Fatal(reply)
		}
	}
	if _, err := a.user.GetMailbox("Blocked"); err == nil {
		t.Fatal("rate-limited CREATE changed the mailbox")
	}
	imapReply(t, i2, "LOGOUT", "OK")
	assertClosed(t, i2)
	i3 := imapWire(t, imapAddr, one.Username, one.Password)
	imapReply(t, i3, "NOOP", "NO")
	iOther := imapWire(t, imapAddr, two.Username, two.Password)
	imapReply(t, iOther, "NOOP", "OK")
	// SMTP still has one admission for the other account despite IMAP activity.
	smtpReply(t, other, "NOOP", 250)
	apiCall(t, h, "POST", one.APIBase+"/messages", map[string]any{"from": "sender@example.test", "to": []string{"recipient@example.test"}, "body": "still available"}, 201)
	if accountCount(t, h, one.APIBase) != 4 {
		t.Fatal("HTTP seeding was affected by protocol limits")
	}
}

func TestCommandRateLimitsRejectMessagePayloads(t *testing.T) {
	cfg := defaultConfig()
	cfg.SMTPAllowInsecureAuth = true
	cfg.AccountLimits = accountLimits{SMTPCommands: 2, SMTPCommandWindow: "1h", IMAPCommands: 1, IMAPCommandWindow: "1h"}
	a := testStore(t, cfg)
	w := authenticatedSMTPWire(t, smtpAddress(t, a), cfg.Username, cfg.Password)
	smtpReply(t, w, "MAIL FROM:<sender@example.test>", 250)
	smtpReply(t, w, "RCPT TO:<recipient@example.test>", 250)
	smtpReply(t, w, "DATA", 451) // Must reject before inviting the body.
	raw := testRaw("limited@test", "limited", "recipient@example.test")
	// A rejected BDAT must drain the chunk and preserve command framing.
	if err := w.PrintfLine("BDAT %d LAST\r\n%sRSET", len(raw), raw); err != nil {
		t.Fatal(err)
	}
	smtpReply(t, w, "", 451)
	smtpReply(t, w, "", 250)
	smtpReply(t, w, "QUIT", 221)

	i := imapWire(t, imapAddress(t, a.service), cfg.Username, cfg.Password)
	imapReply(t, i, "NOOP", "OK")
	// Synchronizing literals must be refused without waiting for a body.
	imapReply(t, i, fmt.Sprintf("APPEND INBOX {%d}", len(raw)), "NO")
	// Non-synchronizing literals must be drained without being stored.
	imapReply(t, i, fmt.Sprintf("APPEND INBOX {%d+}\r\n%s", len(raw), raw), "NO")
	imapReply(t, i, "LOGOUT", "OK")
	if len(a.snapshot()) != 0 {
		t.Fatal("rate-limited payload was stored")
	}
}

func TestCommandRateLimitsRecoverOnSameConnection(t *testing.T) {
	cfg := defaultConfig()
	cfg.SMTPAllowInsecureAuth = true
	cfg.AccountLimits = accountLimits{SMTPCommands: 1, SMTPCommandWindow: "1s", IMAPCommands: 1, IMAPCommandWindow: "1s"}
	a := testStore(t, cfg)
	w := authenticatedSMTPWire(t, smtpAddress(t, a), cfg.Username, cfg.Password)
	i := imapWire(t, imapAddress(t, a.service), cfg.Username, cfg.Password)
	smtpReply(t, w, "NOOP", 250)
	imapReply(t, i, "NOOP", "OK")
	smtpReply(t, w, "NOOP", 451)
	imapReply(t, i, "NOOP", "NO")
	time.Sleep(1100 * time.Millisecond)
	smtpReply(t, w, "NOOP", 250)
	imapReply(t, i, "NOOP", "OK")
	smtpReply(t, w, "NOOP", 451)
	imapReply(t, i, "NOOP", "NO")
}

func TestCommandRateLimitsCountFailedCommands(t *testing.T) {
	cfg := defaultConfig()
	cfg.SMTPAllowInsecureAuth = true
	cfg.AccountLimits = accountLimits{SMTPCommands: 1, SMTPCommandWindow: "1h", IMAPCommands: 1, IMAPCommandWindow: "1h"}
	a := testStore(t, cfg)
	w := authenticatedSMTPWire(t, smtpAddress(t, a), cfg.Username, cfg.Password)
	smtpReply(t, w, "RCPT TO:<recipient@example.test>", 502) // No MAIL yet.
	smtpReply(t, w, "NOOP", 451)
	i := imapWire(t, imapAddress(t, a.service), cfg.Username, cfg.Password)
	imapReply(t, i, "SELECT Missing", "NO")
	if reply := imapReply(t, i, "NOOP", "NO"); !strings.Contains(reply, "[LIMIT]") {
		t.Fatal(reply)
	}
}

func TestCommandRateLimitsAtomicAndWindowBoundaries(t *testing.T) {
	cfg := defaultConfig()
	cfg.AccountLimits = accountLimits{SMTPCommands: 3, SMTPCommandWindow: "1s", IMAPCommands: 5, IMAPCommandWindow: "2s"}
	a := testStore(t, cfg)
	now := time.Now()
	var smtpCount, imapCount atomic.Int32
	var wg sync.WaitGroup
	for range 50 {
		wg.Go(func() {
			if a.allowCommand("smtp", now) {
				smtpCount.Add(1)
			}
			if a.allowCommand("imap", now) {
				imapCount.Add(1)
			}
		})
	}
	wg.Wait()
	if smtpCount.Load() != 3 || imapCount.Load() != 5 {
		t.Fatal("concurrent commands exceeded limits", smtpCount.Load(), imapCount.Load())
	}
	if a.allowCommand("smtp", now.Add(time.Second-time.Nanosecond)) || a.allowCommand("imap", now.Add(time.Second)) {
		t.Fatal("window reset early")
	}
	if !a.allowCommand("smtp", now.Add(time.Second)) || !a.allowCommand("imap", now.Add(2*time.Second)) {
		t.Fatal("rejected commands extended the window")
	}
}

func TestCommandRateLimitConfiguration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("account_limits:\n  smtp_commands: 3\n  smtp_command_window: 1s\n  imap_commands: 5\n  imap_command_window: 2s\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MAILARKY_CONFIG", path)
	cfg, err := loadConfig(nil, io.Discard)
	want := accountLimits{SMTPCommands: 3, SMTPCommandWindow: "1s", IMAPCommands: 5, IMAPCommandWindow: "2s"}
	if err != nil || cfg.AccountLimits != want {
		t.Fatal(cfg.AccountLimits, err)
	}
	t.Setenv("MAILARKY_ACCOUNT_SMTP_COMMANDS", "7")
	t.Setenv("MAILARKY_ACCOUNT_SMTP_COMMAND_WINDOW", "3s")
	t.Setenv("MAILARKY_ACCOUNT_IMAP_COMMANDS", "9")
	t.Setenv("MAILARKY_ACCOUNT_IMAP_COMMAND_WINDOW", "4s")
	cfg, err = loadConfig(nil, io.Discard)
	want = accountLimits{SMTPCommands: 7, SMTPCommandWindow: "3s", IMAPCommands: 9, IMAPCommandWindow: "4s"}
	if err != nil || cfg.AccountLimits != want {
		t.Fatal(cfg.AccountLimits, err)
	}
	cfg, err = loadConfig([]string{"--account-smtp-commands=11", "--account-smtp-command-window=5s", "--account-imap-commands=13", "--account-imap-command-window=6s"}, io.Discard)
	want = accountLimits{SMTPCommands: 11, SMTPCommandWindow: "5s", IMAPCommands: 13, IMAPCommandWindow: "6s"}
	if err != nil || cfg.AccountLimits != want {
		t.Fatal(cfg.AccountLimits, err)
	}
	_, h := testManager(t, cfg)
	if got := createAccount(t, h, "inherited").Limits; got != want {
		t.Fatal("account did not inherit command limits", got)
	}
	for _, protocol := range []string{"smtp", "imap"} {
		for _, limits := range []map[string]any{
			{protocol + "_commands": -1},
			{protocol + "_commands": 1},
			{protocol + "_command_window": "bad"},
			{protocol + "_command_window": "0s"},
			{protocol + "_command_window": "-1s"},
		} {
			apiCall(t, h, "POST", "/api/v1/accounts", map[string]any{"limits": limits}, 400)
		}
		if _, err := loadConfig([]string{"--account-" + protocol + "-commands=-1"}, io.Discard); err == nil {
			t.Fatal("accepted negative command count")
		}
		if _, err := loadConfig([]string{"--account-" + protocol + "-command-window=0s"}, io.Discard); err == nil {
			t.Fatal("accepted zero command window")
		}
	}
}

func TestCommandRateLimitOverridesAndPersistence(t *testing.T) {
	cfg := defaultConfig()
	cfg.Database = filepath.Join(t.TempDir(), "mail.db")
	cfg.AccountLimits = accountLimits{SMTPCommands: 1, SMTPCommandWindow: "1h", IMAPCommands: 1, IMAPCommandWindow: "1h"}
	service, h := testManager(t, cfg)
	var unlimited mailboxDescription
	if err := json.Unmarshal(apiCall(t, h, "POST", "/api/v1/accounts", map[string]any{"limits": map[string]any{}}, 201).Body.Bytes(), &unlimited); err != nil {
		t.Fatal(err)
	}
	if unlimited.Limits != (accountLimits{}) {
		t.Fatal("explicit unlimited limits did not override defaults", unlimited.Limits)
	}
	for range 10 {
		if !service.lookup(unlimited.ID).allowCommand("smtp", time.Now()) || !service.lookup(unlimited.ID).allowCommand("imap", time.Now()) {
			t.Fatal("zero should disable command limiting")
		}
	}
	want := accountLimits{SMTPCommands: 2, SMTPCommandWindow: "2h", IMAPCommands: 3, IMAPCommandWindow: "3h"}
	var limited mailboxDescription
	if err := json.Unmarshal(apiCall(t, h, "POST", "/api/v1/accounts", mailboxCreation{Limits: &want}, 201).Body.Bytes(), &limited); err != nil {
		t.Fatal(err)
	}
	if limited.Limits != want {
		t.Fatal("account override was lost", limited.Limits)
	}
	for range 3 {
		service.lookup(limited.ID).allowCommand("smtp", time.Now())
		service.lookup(limited.ID).allowCommand("imap", time.Now())
	}
	if err := service.Close(); err != nil {
		t.Fatal(err)
	}
	service, _ = testManager(t, cfg)
	a := service.lookup(limited.ID)
	if a.identity.Limits != want || !a.allowCommand("smtp", time.Now()) || !a.allowCommand("imap", time.Now()) {
		t.Fatal("restart should preserve limits and reset counters", a.identity.Limits)
	}
}
