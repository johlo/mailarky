package sandbox

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func xoauthResponse(username, token string) string {
	return base64.StdEncoding.EncodeToString([]byte("user=" + username + "\x01auth=Bearer " + token + "\x01\x01"))
}
func TestXOAUTH2OnSMTPAndIMAP(t *testing.T) {
	cfg := defaultConfig()
	cfg.SMTPAllowInsecureAuth = true
	service, h := testManager(t, cfg)
	expired := time.Now().Add(-time.Second)
	requests := []oauthTokenRequest{{Token: "valid-token"}, {Token: "expired-token", Status: "expired"}, {Token: "rejected-token", Status: "rejected"}, {Token: "past-token", ExpiresAt: &expired}}
	response := apiCall(t, h, "POST", "/api/v1/accounts", mailboxCreation{Username: "oauth@example.test", OAuthTokens: requests}, 201)
	if strings.Contains(response.Body.String(), "valid-token") {
		t.Fatal("token leaked into account description")
	}
	var account mailboxDescription
	json.Unmarshal(response.Body.Bytes(), &account)
	a := service.lookup(account.ID)
	other := createAccount(t, h, "other")
	smtpAddr, imapAddr := smtpAddress(t, a), imapAddress(t, service)
	for _, protocol := range []string{"smtp", "imap"} {
		for _, tc := range []struct{ token, user, status string }{
			{"valid-token", account.Username, "valid"},
			{"expired-token", account.Username, "expired"},
			{"rejected-token", account.Username, "rejected"},
			{"past-token", account.Username, "expired"},
			{"unknown-token", account.Username, "rejected"},
			{"valid-token", other.Username, "rejected"},
		} {
			t.Run(protocol+"/"+tc.token+"/"+tc.user, func(t *testing.T) {
				if protocol == "smtp" {
					w := smtpWire(t, smtpAddr)
					if caps := smtpReply(t, w, "EHLO localhost", 250); !strings.Contains(caps, "XOAUTH2") {
						t.Fatal(caps)
					}
					cmd := "AUTH XOAUTH2 " + xoauthResponse(tc.user, tc.token)
					if tc.status == "valid" {
						smtpReply(t, w, cmd, 235)
						smtpReply(t, w, "MAIL FROM:<sender@example.test>", 250)
						smtpReply(t, w, "RCPT TO:<external@customer.test>", 250)
						smtpReply(t, w, "DATA", 354)
						out := w.DotWriter()
						out.Write(testRaw("oauth@test", "oauth", "external@customer.test"))
						out.Close()
						smtpReply(t, w, "", 250)
						if len(a.snapshot()) != 1 || len(service.lookup(defaultAccountID).snapshot()) != 0 {
							t.Fatal("OAuth SMTP ownership incorrect")
						}
					} else {
						challenge := smtpReply(t, w, cmd, 334)
						decoded, err := base64.StdEncoding.DecodeString(challenge)
						if err != nil || !strings.Contains(string(decoded), `"status":"401"`) {
							t.Fatal(challenge, err)
						}
						w.PrintfLine("")
						reply := smtpReply(t, w, "", 535)
						if !strings.Contains(reply, "5.7.8") || (tc.status == "expired" && !strings.Contains(reply, "expired")) {
							t.Fatal(reply)
						}
						// Failed auth must leave the connection usable for a new token.
						smtpReply(t, w, "AUTH XOAUTH2 "+xoauthResponse(account.Username, "valid-token"), 235)
					}
				} else {
					w := imapWire(t, imapAddr, "", "")
					if caps := imapReply(t, w, "CAPABILITY", "OK"); !strings.Contains(caps, "AUTH=XOAUTH2") {
						t.Fatal(caps)
					}
					cmd := "AUTHENTICATE XOAUTH2 " + xoauthResponse(tc.user, tc.token)
					if tc.status == "valid" {
						imapReply(t, w, cmd, "OK")
						imapReply(t, w, "SELECT Sent", "OK")
					} else {
						w.PrintfLine("a %s", cmd)
						line, err := w.ReadLine()
						if err != nil || !strings.HasPrefix(line, "+ ") {
							t.Fatal(line, err)
						}
						decoded, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(line, "+ "))
						if err != nil || !strings.Contains(string(decoded), `"status":"401"`) {
							t.Fatal(line, err)
						}
						w.PrintfLine("")
						line, err = w.ReadLine()
						code := "AUTHENTICATIONFAILED"
						if tc.status == "expired" {
							code = "EXPIRED"
						}
						if err != nil || !strings.HasPrefix(line, "a NO ["+code+"]") {
							t.Fatal(line, err)
						}
						imapReply(t, w, "AUTHENTICATE XOAUTH2 "+xoauthResponse(account.Username, "valid-token"), "OK")
					}
				}
			})
		}
	}
	// Revoking/rotating tokens affects future auth, including the default account.
	apiCall(t, h, "PUT", account.APIBase+"/oauth-tokens", map[string]any{"tokens": []oauthTokenRequest{{Token: "replacement"}}}, 204)
	if got, _ := service.authenticateToken(account.Username, "valid-token"); got != nil {
		t.Fatal("revoked token accepted")
	}
	if got, _ := service.authenticateToken(account.Username, "replacement"); got != a {
		t.Fatal("replacement token rejected")
	}
	apiCall(t, h, "PUT", defaultAPI+"/oauth-tokens", map[string]any{"tokens": []oauthTokenRequest{{Token: "default-token"}}}, 204)
	if got, _ := service.authenticateToken(mailboxUsername, "default-token"); got != service.lookup(defaultAccountID) {
		t.Fatal("default account token rejected")
	}
}

func TestXOAUTH2ContinuationMalformedAndFaults(t *testing.T) {
	cfg := defaultConfig()
	cfg.SMTPAllowInsecureAuth = true
	a := testStore(t, cfg)
	h := controlHandler(a)
	apiCall(t, h, "PUT", defaultAPI+"/oauth-tokens", map[string]any{"tokens": []oauthTokenRequest{{Token: "test-token"}}}, 204)
	sw := smtpWire(t, smtpAddress(t, a))
	smtpReply(t, sw, "AUTH XOAUTH2 "+base64.StdEncoding.EncodeToString([]byte("not xoauth2")), 535)
	smtpReply(t, sw, "AUTH XOAUTH2", 334)
	smtpReply(t, sw, xoauthResponse(mailboxUsername, "test-token"), 235)
	addr := imapAddress(t, a.service)
	w := imapWire(t, addr, "", "")
	imapReply(t, w, "AUTHENTICATE XOAUTH2 "+base64.StdEncoding.EncodeToString([]byte("not xoauth2")), "NO")
	w.PrintfLine("a AUTHENTICATE XOAUTH2")
	if line, err := w.ReadLine(); err != nil || !strings.HasPrefix(line, "+") {
		t.Fatal(line, err)
	}
	w.PrintfLine("%s", xoauthResponse(mailboxUsername, "test-token"))
	if line, err := w.ReadLine(); err != nil || !strings.HasPrefix(line, "a OK ") {
		t.Fatal(line, err)
	}
	f := rule("reject-third-oauth", "imap", "AUTHENTICATE", "before", map[string]any{"type": "reject", "status": "NO", "response_code": "EXPIRED"})
	f["sequence"] = map[string]any{"steps": []string{"pass", "pass", "apply"}}
	installFault(t, a, false, f)
	for _, status := range []string{"OK", "OK", "NO", "OK"} {
		w := imapWire(t, addr, "", "")
		imapReply(t, w, "AUTHENTICATE XOAUTH2 "+xoauthResponse(mailboxUsername, "test-token"), status)
	}
	got, _ := a.faults.get("reject-third-oauth")
	if got.Matches != 4 || got.Hits != 1 {
		t.Fatal(got)
	}
}

func TestOAuthAndLimitsPersistWithoutPlaintextTokens(t *testing.T) {
	cfg := defaultConfig()
	cfg.Database = filepath.Join(t.TempDir(), "mail.db")
	service, err := openService(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { service.Close() })
	desc, err := service.create(mailboxCreation{Limits: &accountLimits{QuotaMessages: 3}, OAuthTokens: []oauthTokenRequest{{Token: "keep-token"}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Close(); err != nil {
		t.Fatal(err)
	}
	service, err = openService(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	a, status := service.authenticateToken(desc.Username, "keep-token")
	if a == nil || status != "valid" || a.identity.Limits.QuotaMessages != 3 {
		t.Fatal(a, status)
	}
	raw, err := json.Marshal(a.oauthTokens)
	if err != nil || strings.Contains(string(raw), "keep-token") {
		t.Fatal("plaintext credential persisted", err)
	}
	for _, input := range []any{map[string]any{}, map[string]any{"tokens": nil}, map[string]any{"tokens": []oauthTokenRequest{{Token: "a b"}}}, map[string]any{"tokens": []oauthTokenRequest{{Token: "x", Status: "other"}}}, map[string]any{"tokens": []oauthTokenRequest{{Token: "same"}, {Token: "same"}}}} {
		apiCall(t, newAPI(service).handler(), "PUT", desc.APIBase+"/oauth-tokens", input, 400)
	}
	if a, status = service.authenticateToken(desc.Username, "keep-token"); a == nil {
		t.Fatal("invalid replacement changed tokens", status)
	}
	apiCall(t, newAPI(service).handler(), "PUT", desc.APIBase+"/oauth-tokens", map[string]any{"tokens": []oauthTokenRequest{}}, 204)
	if a, _ = service.authenticateToken(desc.Username, "keep-token"); a != nil {
		t.Fatal("empty replacement didn't revoke tokens")
	}
}
