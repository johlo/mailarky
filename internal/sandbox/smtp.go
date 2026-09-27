package sandbox

import (
	"bufio"
	"context"
	"crypto/subtle"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net/mail"
	"os"
	"strings"
	"time"

	"github.com/emersion/go-sasl"
	smtp "github.com/emersion/go-smtp"
	"golang.org/x/crypto/bcrypt"
)

type credentials map[string]string

func readCredentials(path string, inline ...string) (credentials, error) {
	users := credentials{}
	var content string
	if path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		content = string(data)
	}
	for _, list := range inline {
		content += "\n" + strings.Join(strings.Fields(list), "\n")
	}
	scanner := bufio.NewScanner(strings.NewReader(content))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		username, password, ok := strings.Cut(line, ":")
		if !ok || username == "" || password == "" {
			return nil, errors.New("authentication file requires username:password entries")
		}
		users[username] = password
	}
	return users, scanner.Err()
}
func (users credentials) valid(username, password string) bool {
	stored, ok := users[username]
	if !ok {
		return false
	}
	if strings.HasPrefix(stored, "$2") {
		return bcrypt.CompareHashAndPassword([]byte(stored), []byte(password)) == nil
	}
	return subtle.ConstantTimeCompare([]byte(stored), []byte(password)) == 1
}

type smtpBackend struct {
	store   *mailboxBackend
	manager *mailboxManager
	users   credentials
}
type smtpSession struct {
	backend       *smtpBackend
	conn          *smtp.Conn
	from          string
	recipients    []string
	username      string
	authenticated bool
	selected      *mailboxBackend
}

func (b *smtpBackend) NewSession(c *smtp.Conn) (smtp.Session, error) {
	return &smtpSession{backend: b, conn: c}, nil
}
func (s *smtpSession) Reset() { s.from = ""; s.recipients = nil; s.selected = nil }
func (s *smtpSession) store() *mailboxBackend {
	if s.selected != nil {
		return s.selected
	}
	return s.backend.store
}
func (s *smtpSession) Logout() error { return nil }
func (s *smtpSession) AuthMechanisms() []string {
	if len(s.backend.users) > 0 || s.backend.store.config.SMTPAcceptAny {
		return []string{"PLAIN", "LOGIN"}
	}
	return nil
}
func (s *smtpSession) Auth(mechanism string) (sasl.Server, error) {
	authenticate := func(username, password string) error {
		if !s.backend.store.config.SMTPAcceptAny && !s.backend.users.valid(username, password) {
			return smtp.ErrAuthFailed
		}
		s.username = username
		s.authenticated = true
		return nil
	}
	switch mechanism {
	case "PLAIN":
		return sasl.NewPlainServer(func(identity, username, password string) error {
			if identity != "" && identity != username {
				return smtp.ErrAuthFailed
			}
			return authenticate(username, password)
		}), nil
	case "LOGIN":
		return &loginServer{authenticate: authenticate}, nil
	default:
		return nil, smtp.ErrAuthUnknownMechanism
	}
}

type loginServer struct {
	step         int
	username     string
	authenticate func(string, string) error
}

func (s *loginServer) Next(response []byte) ([]byte, bool, error) {
	switch s.step {
	case 0:
		s.step++
		if len(response) == 0 {
			return []byte("Username:"), false, nil
		}
		s.username = string(response)
		s.step++
		return []byte("Password:"), false, nil
	case 1:
		s.username = string(response)
		s.step++
		return []byte("Password:"), false, nil
	case 2:
		return nil, true, s.authenticate(s.username, string(response))
	default:
		return nil, true, smtp.ErrAuthFailed
	}
}
func validEnvelope(address string, empty bool) bool {
	if address == "" {
		return empty
	}
	if strings.ContainsAny(address, "\r\n\x00") {
		return false
	}
	a, err := mail.ParseAddress(address)
	return err == nil && a.Address == address
}

func (s *smtpSession) fault(stage string, raw []byte) error {
	b := s.store()
	m := &storedMessage{EnvelopeFrom: s.from, EnvelopeTo: s.recipients, Folder: b.config.SMTPFolder, Headers: map[string][]string{}}
	if len(raw) > 0 {
		detail, headers, err := parseMessage(raw, time.Now(), s.from, s.recipients)
		if err != nil {
			return &smtp.SMTPError{Code: 554, Message: "Invalid MIME message"}
		}
		m.Detail, m.Headers = detail, headers
	}
	for _, t := range b.toxics.claim("smtp", stage, m) {
		switch t.Type {
		case "smtp_reject":
			b.rejected.Add(1)
			return &smtp.SMTPError{Code: t.Attributes.Code, Message: "Toxic " + t.Name}
		case "smtp_delay":
			if err := waitToxic(context.Background(), b.done, t.Attributes.DelayMS); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *smtpSession) Mail(from string, _ *smtp.MailOptions) error {
	c := s.backend.store.config
	if len(s.backend.users) > 0 && !c.SMTPAcceptAny && !s.authenticated {
		return smtp.ErrAuthRequired
	}
	if c.RequireTLS {
		if _, ok := s.conn.TLSConnectionState(); !ok {
			return &smtp.SMTPError{Code: 530, Message: "TLS is required"}
		}
	}
	if !validEnvelope(from, true) {
		return &smtp.SMTPError{Code: 501, Message: "Invalid envelope sender"}
	}
	s.Reset()
	s.from = from
	// Recipient routing cannot select an account until RCPT TO. Defer sender
	// toxics until then so the default account cannot affect another account.
	if s.backend.manager != nil {
		return nil
	}
	return s.fault("sender", nil)
}
func (s *smtpSession) Rcpt(to string, _ *smtp.RcptOptions) error {
	if !validEnvelope(to, false) {
		return &smtp.SMTPError{Code: 501, Message: "Invalid envelope recipient"}
	}
	if h := s.backend.manager; h != nil {
		target := h.route(to)
		if target == nil {
			return &smtp.SMTPError{Code: 550, Message: "Mailbox is unavailable"}
		}
		if s.selected != nil && s.selected != target {
			return &smtp.SMTPError{Code: 553, Message: "Use separate SMTP transactions for different mailboxes"}
		}
		if s.selected == nil {
			s.selected = target
			if err := s.fault("sender", nil); err != nil {
				s.selected = nil
				return err
			}
		}
	}
	s.recipients = append(s.recipients, to)
	if err := s.fault("recipient", nil); err != nil {
		s.recipients = s.recipients[:len(s.recipients)-1]
		return err
	}
	return nil
}
func (s *smtpSession) Data(reader io.Reader) error {
	b := s.store()
	raw, err := io.ReadAll(io.LimitReader(reader, b.config.MaxSize+1))
	if err != nil {
		return err
	}
	if int64(len(raw)) > b.config.MaxSize {
		b.rejected.Add(1)
		return &smtp.SMTPError{Code: 552, Message: "Message size exceeds limit"}
	}
	if err := s.fault("data", raw); err != nil {
		return err
	}
	if _, err := b.append(raw, appendOptions{Folder: b.config.SMTPFolder, From: s.from, To: s.recipients, Username: s.username, Notify: true}); err != nil {
		b.rejected.Add(1)
		return &smtp.SMTPError{Code: 554, Message: "Message could not be stored"}
	}
	b.accepted.Add(1)
	b.acceptedSize.Add(uint64(len(raw)))
	return nil
}

func newSMTPServer(b *mailboxBackend, managers ...*mailboxManager) (*smtp.Server, error) {
	users, err := readCredentials(b.config.SMTPAuthFile, b.config.SMTPAuth)
	if err != nil {
		return nil, err
	}
	back := &smtpBackend{store: b, users: users}
	if len(managers) > 0 {
		back.manager = managers[0]
	}
	srv := smtp.NewServer(back)
	srv.Addr = b.config.SMTPAddress
	srv.Domain = "mail-sandbox.test"
	srv.MaxMessageBytes = b.config.MaxSize
	srv.MaxRecipients = 1000
	srv.ReadTimeout = time.Minute
	srv.WriteTimeout = time.Minute
	srv.AllowInsecureAuth = b.config.SMTPAllowInsecureAuth
	srv.EnableSMTPUTF8 = true
	if b.config.SMTPTLS != "" {
		certPath, keyPath := b.config.SMTPCert, b.config.SMTPKey
		if certPath == "" {
			certPath = b.config.Cert
		}
		if keyPath == "" {
			keyPath = b.config.Key
		}
		cert, err := tls.LoadX509KeyPair(certPath, keyPath)
		if err != nil {
			return nil, fmt.Errorf("SMTP certificate: %w", err)
		}
		srv.TLSConfig = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	}
	return srv, nil
}
