package sandbox

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/emersion/go-sasl"
	"github.com/johlo/go-imap/v2"
	"github.com/johlo/go-imap/v2/imapserver"
	"github.com/johlo/go-smtp"
	bolt "go.etcd.io/bbolt"
)

// Local test credentials: no token issuance or external provider validation.
type oauthTokenRequest struct {
	Token     string     `json:"token"`
	Status    string     `json:"status,omitempty"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}
type oauthToken struct {
	Digest    [32]byte   `json:"digest"`
	Status    string     `json:"status"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

func prepareOAuthTokens(requests []oauthTokenRequest) ([]oauthToken, error) {
	if len(requests) > 100 {
		return nil, errors.New("at most 100 test OAuth tokens per account")
	}
	tokens := make([]oauthToken, 0, len(requests))
	seen := map[[32]byte]bool{}
	for _, req := range requests {
		if req.Token == "" || len(req.Token) > 1024 {
			return nil, errors.New("test token must contain 1-1024 printable ASCII bytes")
		}
		for _, c := range req.Token {
			if c < 33 || c > 126 {
				return nil, errors.New("test tokens must be printable ASCII without spaces")
			}
		}
		if req.Status == "" {
			req.Status = "valid"
		}
		if req.Status != "valid" && req.Status != "expired" && req.Status != "rejected" {
			return nil, errors.New("token status must be valid, expired or rejected")
		}
		token := oauthToken{Digest: sha256.Sum256([]byte(req.Token)), Status: req.Status}
		if req.ExpiresAt != nil {
			expiry := *req.ExpiresAt
			token.ExpiresAt = &expiry
		}
		if seen[token.Digest] {
			return nil, errors.New("duplicate test token")
		}
		seen[token.Digest] = true
		tokens = append(tokens, token)
	}
	return tokens, nil
}
func (a *Account) loadOAuthTokens() error {
	if a.db == nil {
		return nil
	}
	return a.db.View(func(tx *bolt.Tx) error {
		bucket := tx.Bucket([]byte("oauth"))
		if bucket == nil || bucket.Get([]byte("tokens")) == nil {
			return nil
		}
		return json.Unmarshal(bucket.Get([]byte("tokens")), &a.oauthTokens)
	})
}
func (a *Account) setOAuthTokens(tokens []oauthToken) error {
	a.oauthMu.Lock()
	defer a.oauthMu.Unlock()
	select {
	case <-a.done:
		return errAccountClosed
	default:
	}
	if a.db != nil {
		data, err := json.Marshal(tokens)
		if err != nil {
			return err
		}
		if err := a.db.Update(func(tx *bolt.Tx) error {
			b, err := tx.CreateBucketIfNotExists([]byte("oauth"))
			if err != nil {
				return err
			}
			return b.Put([]byte("tokens"), data)
		}); err != nil {
			return err
		}
	}
	a.oauthTokens = tokens
	return nil
}
func (a *accountAPI) oauthTokens(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Tokens []oauthTokenRequest `json:"tokens"`
	}
	if err := decodeJSON(w, r, 256<<10, &req); err != nil {
		apiError(w, 400, err)
		return
	}
	if req.Tokens == nil {
		apiError(w, 400, errors.New("tokens array is required; use [] to revoke all tokens"))
		return
	}
	tokens, err := prepareOAuthTokens(req.Tokens)
	if err != nil {
		apiError(w, 400, err)
		return
	}
	if err := a.account.setOAuthTokens(tokens); errors.Is(err, errAccountClosed) {
		apiError(w, 404, errors.New("account not found"))
		return
	} else if err != nil {
		apiError(w, 500, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
func (h *Service) authenticateToken(username, token string) (*Account, string) {
	a := h.accountByUsername(username)
	if a == nil {
		return nil, "rejected"
	}
	a.oauthMu.RLock()
	defer a.oauthMu.RUnlock()
	select {
	case <-a.done:
		return nil, "rejected"
	default:
	}
	digest := sha256.Sum256([]byte(token))
	for _, candidate := range a.oauthTokens {
		if candidate.Digest != digest {
			continue
		}
		status := candidate.Status
		if status == "valid" && candidate.ExpiresAt != nil && !time.Now().Before(*candidate.ExpiresAt) {
			status = "expired"
		}
		if status != "valid" {
			return nil, status
		}
		return a, status
	}
	return nil, "rejected"
}

type oauthFailure struct{ response error }

func (e *oauthFailure) Error() string { return e.response.Error() }
func oauthAuthError(protocol, status string) error {
	message := "OAuth token rejected"
	code := imap.ResponseCodeAuthenticationFailed
	if status == "expired" {
		message, code = "OAuth token expired", imap.ResponseCodeExpired
	}
	var response error = &imap.Error{Type: imap.StatusResponseTypeNo, Code: code, Text: message}
	if protocol == "smtp" {
		response = &smtp.SMTPError{Code: 535, EnhancedCode: smtp.EnhancedCode{5, 7, 8}, Message: message}
	}
	return &oauthFailure{response: response}
}

// XOAUTH2 uses user=...^Aauth=Bearer ...^A^A, unlike OAUTHBEARER's GS2 header.
// A rejected token gets the JSON challenge used by Gmail, followed by the
// protocol's authentication error after the client's continuation response.
type xoauth2Server struct {
	authenticate func(string, string) error
	invalid      error
	started      bool
	failure      error
}

func (s *xoauth2Server) Next(response []byte) ([]byte, bool, error) {
	if s.failure != nil {
		return nil, true, s.failure
	}
	if !s.started && response == nil {
		s.started = true
		return nil, false, nil
	}
	s.started = true
	parts := strings.Split(string(response), "\x01")
	if len(parts) != 4 || !strings.HasPrefix(parts[0], "user=") || !strings.HasPrefix(parts[1], "auth=Bearer ") || parts[2] != "" || parts[3] != "" {
		return nil, true, s.invalid
	}
	username, token := strings.TrimPrefix(parts[0], "user="), strings.TrimPrefix(parts[1], "auth=Bearer ")
	if username == "" || len(username) > 255 || token == "" || len(token) > 1024 {
		return nil, true, s.invalid
	}
	err := s.authenticate(username, token)
	var failure *oauthFailure
	if errors.As(err, &failure) {
		s.failure = failure.response
		return []byte(`{"status":"401","schemes":"bearer"}`), false, nil
	}
	return nil, true, err
}

var _ imapserver.SessionSASL = (*imapSession)(nil)

func (s *imapSession) AuthenticateMechanisms() []string { return []string{"PLAIN", "XOAUTH2"} }
func (s *imapSession) Authenticate(mechanism string) (sasl.Server, error) {
	switch mechanism {
	case "PLAIN":
		return sasl.NewPlainServer(func(identity, username, password string) error {
			if identity != "" && identity != username {
				return imapserver.ErrAuthFailed
			}
			return s.Login(username, password)
		}), nil
	case "XOAUTH2":
		return &xoauth2Server{invalid: imapserver.ErrAuthFailed, authenticate: func(username, token string) error {
			if err := s.authenticationFault(username); err != nil {
				return err
			}
			account, status := s.service.authenticateToken(username, token)
			if account == nil {
				return oauthAuthError("imap", status)
			}
			return s.bindAccount(account)
		}}, nil
	default:
		return nil, imapNo(errors.New("SASL mechanism not supported"))
	}
}
