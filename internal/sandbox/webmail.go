package sandbox

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const webmailLifetime = time.Hour

type webmailCredentials struct {
	Username  string    `json:"username"`
	Password  string    `json:"password"`
	ExpiresAt time.Time `json:"expires_at"`
}

// The control API can issue an account-scoped credential without recovering or
// changing the test's password. The signing key lives only in this process;
// restarting Mailarky or deleting the account invalidates future logins.
func (a *Account) newWebmailCredentials(now time.Time) (webmailCredentials, error) {
	var nonce [8]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return webmailCredentials{}, err
	}
	expires := now.UTC().Add(webmailLifetime).Truncate(time.Second)
	payload := "mw1." + strconv.FormatInt(expires.Unix(), 36) + "." + base64.RawURLEncoding.EncodeToString(nonce[:])
	password := payload + "." + base64.RawURLEncoding.EncodeToString(a.webmailMAC(payload))
	return webmailCredentials{Username: a.identity.Username, Password: password, ExpiresAt: expires}, nil
}

func (a *Account) webmailMAC(payload string) []byte {
	mac := hmac.New(sha256.New, a.service.webmailKey[:])
	mac.Write([]byte(a.identity.ID + "\x00" + a.identity.Username + "\x00" + payload))
	return mac.Sum(nil)
}

func (a *Account) validWebmailPassword(password string, now time.Time) bool {
	if len(password) > 72 || !strings.HasPrefix(password, "mw1.") {
		return false
	}
	parts := strings.Split(password, ".")
	if len(parts) != 4 {
		return false
	}
	expires, err := strconv.ParseInt(parts[1], 36, 64)
	if err != nil || !now.Before(time.Unix(expires, 0)) {
		return false
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[3])
	if err != nil {
		return false
	}
	return hmac.Equal(signature, a.webmailMAC(strings.Join(parts[:3], ".")))
}

func (a *accountAPI) webmailLogin(w http.ResponseWriter, r *http.Request) {
	select {
	case <-a.account.done:
		apiError(w, http.StatusNotFound, errors.New("account not found"))
		return
	default:
	}
	credentials, err := a.account.newWebmailCredentials(time.Now())
	if err != nil {
		apiError(w, http.StatusInternalServerError, errors.New("could not create webmail credentials"))
		return
	}
	jsonResponse(w, http.StatusCreated, credentials)
}
