package sandbox

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
)

func readHistory(t *testing.T, h http.Handler, base, query string) commandHistoryPage {
	t.Helper()
	var page commandHistoryPage
	w := apiCall(t, h, "GET", base+"/history"+query, nil, 200)
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	return page
}

func TestHistoryRateLimitsConnectionsAndIsolation(t *testing.T) {
	cfg := defaultConfig()
	cfg.SMTPAllowInsecureAuth = true
	cfg.AccountLimits = accountLimits{SMTPCommands: 1, SMTPCommandWindow: "1h", IMAPCommands: 1, IMAPCommandWindow: "1h"}
	service, h := testManager(t, cfg)
	one, two := createAccount(t, h, "one"), createAccount(t, h, "two")
	addr := smtpAddress(t, service.lookup(one.ID))
	w := authenticatedSMTPWire(t, addr, one.Username, one.Password)
	smtpReply(t, w, "NOOP", 250)
	smtpReply(t, w, "NOOP", 451)
	smtpReply(t, w, "QUIT", 221)
	w = authenticatedSMTPWire(t, addr, one.Username, one.Password)
	smtpReply(t, w, "NOOP", 451)
	other := authenticatedSMTPWire(t, addr, two.Username, two.Password)
	smtpReply(t, other, "NOOP", 250)
	p := readHistory(t, h, one.APIBase, "?protocol=smtp&command=noop")
	if len(p.Commands) != 3 || p.Commands[0].Response != "250 2.0.0" || p.Commands[1].Response != "451 4.7.0" || p.Commands[2].Response != "451 4.7.0" {
		t.Fatal(p)
	}
	if p.Commands[0].ConnectionID != p.Commands[1].ConnectionID || p.Commands[0].ConnectionID == p.Commands[2].ConnectionID {
		t.Fatal("history lost connection identity", p.Commands)
	}
	for i, entry := range p.Commands {
		if entry.Outcome != "response" || entry.ConnectionID == "" || entry.CompletedAt.Before(entry.StartedAt) || entry.DurationMS < 0 || entry.ID == 0 {
			t.Fatal(entry)
		}
		if i > 0 && (entry.ID <= p.Commands[i-1].ID || entry.StartedAt.Before(p.Commands[i-1].CompletedAt)) {
			t.Fatal("commands not ordered", p.Commands)
		}
	}
	filtered := readHistory(t, h, one.APIBase, "?protocol=smtp&command=NOOP&connection_id="+p.Commands[0].ConnectionID)
	if len(filtered.Commands) != 2 {
		t.Fatal(filtered)
	}
	if q := readHistory(t, h, two.APIBase, "?command=NOOP"); len(q.Commands) != 1 || q.Commands[0].Response != "250 2.0.0" {
		t.Fatal("account histories leaked", q)
	}
	iw := imapWire(t, imapAddress(t, service), one.Username, one.Password)
	imapReply(t, iw, "NOOP", "OK")
	imapReply(t, iw, "NOOP", "NO")
	imapReply(t, iw, "LOGOUT", "OK")
	q := readHistory(t, h, one.APIBase, "?protocol=imap&command=NOOP")
	if len(q.Commands) != 2 || q.Commands[0].Response != "OK" || q.Commands[1].Response != "NO [LIMIT]" || q.Commands[0].ConnectionID == p.Commands[0].ConnectionID {
		t.Fatal(q)
	}
	for _, command := range []string{"QUIT", "LOGOUT"} {
		q = readHistory(t, h, one.APIBase, "?command="+command)
		if len(q.Commands) != 1 || !q.Commands[0].ConnectionClosed {
			t.Fatal(q)
		}
	}
	before := readHistory(t, h, one.APIBase, "")
	after := readHistory(t, h, one.APIBase, "")
	if before.NextAfter != after.NextAfter || len(before.Commands) != len(after.Commands) {
		t.Fatal("HTTP history reads created history entries")
	}
	apiCall(t, h, "DELETE", one.APIBase+"/history", nil, 204)
	if q := readHistory(t, h, one.APIBase, ""); len(q.Commands) != 0 || q.NextAfter != before.NextAfter {
		t.Fatal("clear should preserve the cursor", q)
	}
	smtpReply(t, w, "NOOP", 451) // Clearing history must not reset rate limits.
	if q := readHistory(t, h, one.APIBase, fmt.Sprintf("?after=%d", before.NextAfter)); len(q.Commands) != 1 || q.Commands[0].Response != "451 4.7.0" {
		t.Fatal(q)
	}
}

func TestHistoryAuthenticationAndRedaction(t *testing.T) {
	cfg := defaultConfig()
	cfg.SMTPAllowInsecureAuth = true
	cfg.Password = "secret-password-in-history-test"
	a := testStore(t, cfg)
	h := controlHandler(a)
	w := smtpWire(t, smtpAddress(t, a))
	smtpReply(t, w, "NOOP", 250)
	if p := readHistory(t, h, defaultAPI, ""); len(p.Commands) != 0 {
		t.Fatal("unidentified traffic was attributed to default", p)
	}
	for _, attempt := range []struct {
		password string
		code     int
	}{{"wrong-private-password", 535}, {cfg.Password, 235}} {
		auth := base64.StdEncoding.EncodeToString([]byte("\x00" + cfg.Username + "\x00" + attempt.password))
		smtpReply(t, w, "AUTH PLAIN "+auth, attempt.code)
	}
	// Unknown commands and echoed error text must not leak arbitrary input.
	smtpReply(t, w, "SECR private-command-token", 500)
	iw := imapWire(t, imapAddress(t, a.service), "", "")
	imapReply(t, iw, fmt.Sprintf("LOGIN %q %q", cfg.Username, "wrong-private-password"), "NO")
	imapReply(t, iw, fmt.Sprintf("LOGIN %q %q", cfg.Username, cfg.Password), "OK")
	imapReply(t, iw, "SELECT INBOX", "OK")
	imapReply(t, iw, "UID SEARCH ALL", "OK")
	p := readHistory(t, h, defaultAPI, "")
	if len(p.Commands) != 7 || p.Commands[0].Response != "535 5.7.8" || p.Commands[1].Response != "235 2.0.0" || p.Commands[2].Command != "UNKNOWN" {
		t.Fatal(p)
	}
	if p.Commands[6].Command != "UID SEARCH" || p.Commands[6].Mailbox != "INBOX" {
		t.Fatal(p.Commands[6])
	}
	raw, _ := json.Marshal(p)
	for _, secret := range []string{cfg.Password, "wrong-private-password", "SECR", "private-command-token", base64.StdEncoding.EncodeToString([]byte("\x00" + cfg.Username + "\x00" + cfg.Password))} {
		if strings.Contains(string(raw), secret) {
			t.Fatal("history exposed a credential or arbitrary command text")
		}
	}
}

func TestHistoryRecordsFinalFaultOutcome(t *testing.T) {
	for _, protocol := range []string{"smtp", "imap"} {
		for _, kind := range []string{"before", "after", "disconnect", "silent", "raw"} {
			t.Run(protocol+"/"+kind, func(t *testing.T) {
				cfg := defaultConfig()
				cfg.SMTPAllowInsecureAuth = true
				a := testStore(t, cfg)
				h := controlHandler(a)
				params := map[string]any{"type": "reject", "message": "private-reply-text"}
				response, outcome, phase, closed := "451 4.7.0", "response", "before", false
				if protocol == "smtp" {
					params["code"], params["enhanced_code"] = 451, "4.7.0"
				} else {
					params["status"], params["response_code"] = "NO", "LIMIT"
					response = "NO [LIMIT]"
				}
				switch kind {
				case "after":
					phase = "after"
				case "disconnect", "silent":
					params = map[string]any{"type": "disconnect", "silent": kind == "silent"}
					outcome, closed, response = "disconnect", true, ""
					if kind == "disconnect" {
						response = "BYE"
						if protocol == "smtp" {
							response = "421 4.3.2"
						}
					}
				case "raw":
					raw := "451 private-reply-text"
					if protocol == "imap" {
						raw = "{tag} NO private-reply-text"
					}
					params = map[string]any{"type": "response", "response": raw}
					outcome, response = "raw", ""
				}
				installFault(t, a, false, rule("outcome", protocol, "NOOP", phase, params))
				if protocol == "smtp" {
					w := authenticatedSMTPWire(t, smtpAddress(t, a), cfg.Username, cfg.Password)
					apiCall(t, h, "DELETE", defaultAPI+"/history", nil, 204)
					if closed {
						w.PrintfLine("NOOP")
						if kind != "silent" {
							smtpReply(t, w, "", 421)
						}
						assertClosed(t, w)
					} else {
						smtpReply(t, w, "NOOP", 451)
					}
				} else {
					w := imapWire(t, imapAddress(t, a.service), cfg.Username, cfg.Password)
					apiCall(t, h, "DELETE", defaultAPI+"/history", nil, 204)
					if closed {
						w.PrintfLine("a NOOP")
						if kind != "silent" {
							line, err := w.ReadLine()
							if err != nil || !strings.HasPrefix(line, "* BYE") {
								t.Fatal(line, err)
							}
						}
						assertClosed(t, w)
					} else {
						imapReply(t, w, "NOOP", "NO")
					}
				}
				p := readHistory(t, h, defaultAPI, "")
				if len(p.Commands) != 1 {
					t.Fatal("expected one terminal outcome", p)
				}
				e := p.Commands[0]
				if e.Command != "NOOP" || e.Outcome != outcome || e.Response != response || e.ConnectionClosed != closed {
					t.Fatal(e)
				}
			})
		}
	}
}

func TestHistoryDataAndRecipientOutcomes(t *testing.T) {
	cfg := defaultConfig()
	cfg.SMTPAllowInsecureAuth = true
	a := testStore(t, cfg)
	h := controlHandler(a)
	f := rule("recipient", "smtp", "RCPT", "before", map[string]any{"type": "reject", "code": 550})
	f["filter"] = map[string]any{"message": map[string]string{"to": "bad@example.test"}}
	installFault(t, a, false, f)
	w := authenticatedSMTPWire(t, smtpAddress(t, a), cfg.Username, cfg.Password)
	smtpReply(t, w, "MAIL FROM:<sender@example.test>", 250)
	smtpReply(t, w, "RCPT TO:<bad@example.test>", 550)
	smtpReply(t, w, "RCPT TO:<good@example.test>", 250)
	smtpReply(t, w, "DATA", 354)
	if p := readHistory(t, h, defaultAPI, "?command=DATA"); len(p.Commands) != 0 {
		t.Fatal("DATA continuation recorded as completion", p)
	}
	writer := w.DotWriter()
	if _, err := writer.Write(testRaw("history@test", "private-body-text", "good@example.test")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	smtpReply(t, w, "", 250)
	p := readHistory(t, h, defaultAPI, "?command=RCPT")
	if len(p.Commands) != 2 || p.Commands[0].Recipient != "bad@example.test" || p.Commands[0].Response != "550 5.0.0" || p.Commands[1].Recipient != "good@example.test" {
		t.Fatal(p)
	}
	p = readHistory(t, h, defaultAPI, "?command=DATA")
	if len(p.Commands) != 1 || !strings.HasPrefix(p.Commands[0].Response, "250") {
		t.Fatal(p)
	}
	if strings.Contains(apiCall(t, h, "GET", defaultAPI+"/history", nil, 200).Body.String(), "private-body-text") {
		t.Fatal("message content exposed")
	}
}

func TestHistoryDelayAndClearInFlight(t *testing.T) {
	a := testStore(t, defaultConfig())
	h := controlHandler(a)
	w := imapWire(t, imapAddress(t, a.service), mailboxUsername, mailboxPassword)
	installFault(t, a, false, rule("slow", "imap", "NOOP", "before", map[string]any{"type": "delay", "delay_ms": 100}))
	imapReply(t, w, "NOOP", "OK")
	p := readHistory(t, h, defaultAPI, "?command=NOOP")
	if len(p.Commands) != 1 || p.Commands[0].DurationMS < 90 {
		t.Fatal(p)
	}
	if err := w.PrintfLine("a NOOP"); err != nil {
		t.Fatal(err)
	}
	awaitFault(t, a.faults, "slow", 2)
	apiCall(t, h, "DELETE", defaultAPI+"/history", nil, 204)
	if line, err := w.ReadLine(); err != nil || !strings.HasPrefix(line, "a OK") {
		t.Fatal(line, err)
	}
	if p := readHistory(t, h, defaultAPI, ""); len(p.Commands) != 0 {
		t.Fatal("cleared command reappeared", p)
	}
	imapReply(t, w, "NOOP", "OK")
	if p := readHistory(t, h, defaultAPI, ""); len(p.Commands) != 1 {
		t.Fatal(p)
	}
}

func TestHistoryRetentionCursorsAndValidation(t *testing.T) {
	a := testStore(t, defaultConfig())
	h := controlHandler(a)
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			r := newCommandRecorder()
			for range 150 {
				r.start("smtp", "NOOP", a)
				r.finish("response", "250 2.0.0", false)
			}
		})
	}
	wg.Wait()
	p := readHistory(t, h, defaultAPI, "?limit=1000")
	if len(p.Commands) != 1000 || p.Dropped != 200 || p.NextAfter != 1200 || p.HasMore || p.Capacity != 1000 || p.Commands[0].ID != 201 {
		t.Fatal(p)
	}
	var ids []uint64
	after := uint64(0)
	for {
		page := readHistory(t, h, defaultAPI, fmt.Sprintf("?limit=37&after=%d", after))
		for _, entry := range page.Commands {
			ids = append(ids, entry.ID)
		}
		after = page.NextAfter
		if !page.HasMore {
			break
		}
	}
	if len(ids) != 1000 {
		t.Fatal(len(ids))
	}
	for i, id := range ids {
		if id != uint64(i+201) {
			t.Fatal("cursor skipped or repeated a command", i, id)
		}
	}
	if q := readHistory(t, h, defaultAPI, "?protocol=imap"); len(q.Commands) != 0 || q.NextAfter != 1200 {
		t.Fatal(q)
	}
	for _, query := range []string{"?limit=0", "?limit=1001", "?limit=bad", "?limit=", "?after=-1", "?after=bad", "?after=", "?protocol=http"} {
		apiCall(t, h, "GET", defaultAPI+"/history"+query, nil, 400)
	}
	apiCall(t, h, "GET", "/api/v1/accounts/missing/history", nil, 404)
	apiCall(t, h, "DELETE", "/api/v1/accounts/missing/history", nil, 404)
	q := readHistory(t, h, defaultAPI, "?command="+url.QueryEscape("UID FETCH"))
	if len(q.Commands) != 0 {
		t.Fatal(q)
	}
	apiCall(t, h, "DELETE", defaultAPI+"/history", nil, 204)
	q = readHistory(t, h, defaultAPI, "")
	if len(q.Commands) != 0 || q.Dropped != 0 || q.NextAfter != 1200 {
		t.Fatal(q)
	}
}

func TestHistoryOAuthChallengesAndIdle(t *testing.T) {
	cfg := defaultConfig()
	cfg.SMTPAllowInsecureAuth = true
	a := testStore(t, cfg)
	h := controlHandler(a)
	apiCall(t, h, "PUT", defaultAPI+"/oauth-tokens", map[string]any{"tokens": []oauthTokenRequest{{Token: "private-oauth-token"}}}, 204)
	w := smtpWire(t, smtpAddress(t, a))
	smtpReply(t, w, "AUTH XOAUTH2 "+xoauthResponse(cfg.Username, "private-invalid-token"), 334)
	if p := readHistory(t, h, defaultAPI, ""); len(p.Commands) != 0 {
		t.Fatal("challenge recorded as a result", p)
	}
	w.PrintfLine("")
	smtpReply(t, w, "", 535)
	smtpReply(t, w, "AUTH XOAUTH2 "+xoauthResponse(cfg.Username, "private-oauth-token"), 235)
	addr := imapAddress(t, a.service)
	iw := imapWire(t, addr, "", "")
	iw.PrintfLine("a AUTHENTICATE XOAUTH2 %s", xoauthResponse(cfg.Username, "private-invalid-token"))
	if line, err := iw.ReadLine(); err != nil || !strings.HasPrefix(line, "+ ") {
		t.Fatal(line, err)
	}
	if p := readHistory(t, h, defaultAPI, "?protocol=imap"); len(p.Commands) != 0 {
		t.Fatal(p)
	}
	iw.PrintfLine("")
	if line, err := iw.ReadLine(); err != nil || !strings.HasPrefix(line, "a NO ") {
		t.Fatal(line, err)
	}
	imapReply(t, iw, "AUTHENTICATE XOAUTH2 "+xoauthResponse(cfg.Username, "private-oauth-token"), "OK")
	p := readHistory(t, h, defaultAPI, "")
	if len(p.Commands) != 4 {
		t.Fatal("SASL should produce one entry per attempt", p)
	}
	raw, _ := json.Marshal(p)
	for _, secret := range []string{"private-oauth-token", "private-invalid-token", xoauthResponse(cfg.Username, "private-oauth-token"), xoauthResponse(cfg.Username, "private-invalid-token")} {
		if strings.Contains(string(raw), secret) {
			t.Fatal("OAuth secret exposed")
		}
	}
	imapReply(t, iw, "SELECT INBOX", "OK")
	startIdle(t, iw)
	if p := readHistory(t, h, defaultAPI, "?command=IDLE"); len(p.Commands) != 0 {
		t.Fatal("active IDLE is not complete", p)
	}
	finishIdle(t, iw)
	if p := readHistory(t, h, defaultAPI, "?command=IDLE"); len(p.Commands) != 1 || p.Commands[0].Response != "OK" {
		t.Fatal(p)
	}
	installFault(t, a, false, rule("idle-close", "imap", "IDLE", "active", map[string]any{"type": "disconnect", "after_ms": 20, "silent": true}))
	startIdle(t, iw)
	assertClosed(t, iw)
	p = readHistory(t, h, defaultAPI, "?command=IDLE")
	if len(p.Commands) != 2 || p.Commands[1].Outcome != "disconnect" || p.Commands[1].Response != "" || !p.Commands[1].ConnectionClosed {
		t.Fatal(p)
	}
}

func TestHistoryLostAcknowledgementAndBDAT(t *testing.T) {
	cfg := defaultConfig()
	cfg.SMTPAllowInsecureAuth = true
	a := testStore(t, cfg)
	h := controlHandler(a)
	addr := smtpAddress(t, a)
	w := authenticatedSMTPWire(t, addr, cfg.Username, cfg.Password)
	smtpReply(t, w, "MAIL FROM:<sender@example.test>", 250)
	smtpReply(t, w, "RCPT TO:<recipient@example.test>", 250)
	raw := testRaw("chunks@test", "chunked content", "recipient@example.test")
	for i, chunk := range [][]byte{raw[:20], raw[20:]} {
		last := ""
		if i == 1 {
			last = " LAST"
		}
		if _, err := fmt.Fprintf(w.W, "BDAT %d%s\r\n%s", len(chunk), last, chunk); err != nil {
			t.Fatal(err)
		}
		if err := w.W.Flush(); err != nil {
			t.Fatal(err)
		}
		smtpReply(t, w, "", 250)
	}
	if p := readHistory(t, h, defaultAPI, "?command=BDAT"); len(p.Commands) != 2 {
		t.Fatal(p)
	}
	installFault(t, a, false, rule("lost-ack", "smtp", "DATA", "after", map[string]any{"type": "disconnect", "silent": true}))
	smtpReply(t, w, "MAIL FROM:<sender@example.test>", 250)
	smtpReply(t, w, "RCPT TO:<recipient@example.test>", 250)
	smtpReply(t, w, "DATA", 354)
	writer := w.DotWriter()
	writer.Write(testRaw("lost@test", "stored but unacknowledged", "recipient@example.test"))
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	assertClosed(t, w)
	p := readHistory(t, h, defaultAPI, "?command=DATA")
	if len(p.Commands) != 1 || p.Commands[0].Outcome != "disconnect" || p.Commands[0].Response != "" || len(a.snapshot()) != 2 {
		t.Fatal(p)
	}
}

func TestHistoryHTTPProtectionAndRejectedLogout(t *testing.T) {
	cfg := defaultConfig()
	cfg.HTTPAuth, cfg.Webroot, cfg.SMTPAllowInsecureAuth = "admin:secret", "/mail", true
	service, h := testManager(t, cfg)
	for _, method := range []string{"GET", "DELETE"} {
		path := "/mail" + defaultAPI + "/history"
		apiCall(t, h, method, path, nil, 401)
		req := httptest.NewRequest(method, path, nil)
		req.SetBasicAuth("admin", "secret")
		res := httptest.NewRecorder()
		h.ServeHTTP(res, req)
		want := 200
		if method == "DELETE" {
			want = 204
		}
		if res.Code != want {
			t.Fatal(res.Code, res.Body.String())
		}
	}
	// A rejected logout keeps the connection open and must be reported that way.
	a := service.lookup(defaultAccountID)
	f := faultRule{Name: "quit", Enabled: true, Trigger: faultTrigger{Protocol: "smtp", Command: "QUIT", Phase: "before"}, Action: faultAction{Parameters: &rejectAction{Type: "reject", Code: 451}}}
	if err := a.faults.put(f, true, "account"); err != nil {
		t.Fatal(err)
	}
	w := authenticatedSMTPWire(t, smtpAddress(t, a), cfg.Username, cfg.Password)
	smtpReply(t, w, "QUIT", 451)
	smtpReply(t, w, "NOOP", 250)
	p := a.history.page(historyFilter{command: "QUIT", limit: 100})
	if len(p.Commands) != 1 || p.Commands[0].ConnectionClosed {
		t.Fatal(p)
	}
	f = faultRule{Name: "logout", Enabled: true, Trigger: faultTrigger{Protocol: "imap", Command: "LOGOUT", Phase: "before"}, Action: faultAction{Parameters: &rejectAction{Type: "reject", Status: "NO"}}}
	if err := a.faults.put(f, true, "account"); err != nil {
		t.Fatal(err)
	}
	iw := imapWire(t, imapAddress(t, service), cfg.Username, cfg.Password)
	imapReply(t, iw, "LOGOUT", "NO")
	imapReply(t, iw, "NOOP", "OK")
	p = a.history.page(historyFilter{command: "LOGOUT", limit: 100})
	if len(p.Commands) != 1 || p.Commands[0].ConnectionClosed {
		t.Fatal(p)
	}
}

func TestHistoryCompletionCursorsAndLateAssociation(t *testing.T) {
	a := testStore(t, defaultConfig())
	slow, fast := newCommandRecorder(), newCommandRecorder()
	slow.start("imap", "IDLE", a)
	fast.start("smtp", "NOOP", a)
	fast.finish("response", "250 2.0.0", false)
	p := a.history.page(historyFilter{limit: 100})
	if len(p.Commands) != 1 {
		t.Fatal(p)
	}
	slow.finish("response", "OK", false)
	later := a.history.page(historyFilter{after: p.NextAfter, limit: 100})
	if len(later.Commands) != 1 || later.Commands[0].Command != "IDLE" || !later.Commands[0].StartedAt.Before(p.Commands[0].StartedAt) {
		t.Fatal("poll cursor lost a command that completed out of start order", later)
	}
	// AUTH may identify its account only after history has been cleared.
	slow.start("smtp", "AUTH", nil)
	a.history.clear()
	slow.attach(a)
	slow.finish("response", "235 2.0.0", false)
	if p := a.history.page(historyFilter{limit: 100}); len(p.Commands) != 0 {
		t.Fatal("late authentication association resurrected a cleared command", p)
	}
}
