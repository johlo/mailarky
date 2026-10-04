package sandbox

import (
	"bufio"
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
	"github.com/johlo/go-smtp"
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

type smtpBackend struct{ service *Service }
type smtpDelivery struct {
	account    *Account
	recipients []string
}
type smtpSession struct {
	backend       *smtpBackend
	conn          *smtp.Conn
	from          string
	recipients    []string
	username      string
	authenticated *Account
	deliveries    []*smtpDelivery
	completed     *storedMessage
}

func (b *smtpBackend) NewSession(c *smtp.Conn) (smtp.Session, error) {
	return &smtpSession{backend: b, conn: c}, nil
}
func (s *smtpSession) Reset()                   { s.from = ""; s.recipients = nil; s.deliveries = nil; s.completed = nil }
func (s *smtpSession) Logout() error            { return nil }
func (s *smtpSession) AuthMechanisms() []string { return []string{"PLAIN", "LOGIN"} }
func (s *smtpSession) Auth(mechanism string) (sasl.Server, error) {
	authenticate := func(username, password string) error {
		if err := s.authenticationFault(username); err != nil {
			return err
		}
		account := s.backend.service.authenticate(username, password)
		if account == nil {
			return smtp.ErrAuthFailed
		}
		s.username = username
		s.authenticated = account
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

func (s *smtpSession) envelope(recipients []string) *storedMessage {
	return &storedMessage{EnvelopeFrom: s.from, EnvelopeTo: recipients, Headers: map[string][]string{}}
}
func (s *smtpSession) Mail(from string, _ *smtp.MailOptions) error {
	c := s.backend.service.config
	if c.SMTPRequireAuth && s.authenticated == nil {
		return smtp.ErrAuthRequired
	}
	if c.RequireTLS {
		if _, ok := s.conn.TLSConnectionState(); !ok {
			return &smtp.SMTPError{Code: 530, Message: "STARTTLS is required"}
		}
	}
	if !validEnvelope(from, true) {
		return &smtp.SMTPError{Code: 501, Message: "Invalid envelope sender"}
	}
	s.Reset()
	s.from = from
	// MAIL faults run at MAIL. Without AUTH the account is not yet known.
	return s.faults("MAIL", "before", s.authenticated, s.envelope(nil), true)
}
func (s *smtpSession) Rcpt(to string, _ *smtp.RcptOptions) error {
	if !validEnvelope(to, false) {
		return &smtp.SMTPError{Code: 501, Message: "Invalid envelope recipient"}
	}
	account := s.authenticated
	if account == nil {
		account = s.backend.service.route(to)
	}
	if account == nil {
		return &smtp.SMTPError{Code: 550, Message: "Account is unavailable"}
	}
	select {
	case <-account.done:
		return &smtp.SMTPError{Code: 550, Message: "Account is unavailable"}
	default:
	}
	// RCPT matches only this recipient, not previously accepted recipients.
	if err := s.faults("RCPT", "before", account, s.envelope([]string{to}), true); err != nil {
		return err
	}
	s.conn.HookData = smtpCommandContext{username: s.username, accounts: []*smtpDelivery{{account: account, recipients: []string{to}}}, message: s.envelope([]string{to})}
	s.recipients = append(s.recipients, to)
	for _, delivery := range s.deliveries {
		if delivery.account == account {
			delivery.recipients = append(delivery.recipients, to)
			return nil
		}
	}
	s.deliveries = append(s.deliveries, &smtpDelivery{account: account, recipients: []string{to}})
	return nil
}
func (s *smtpSession) Data(reader io.Reader) error {
	raw, err := io.ReadAll(io.LimitReader(reader, s.backend.service.config.MaxSize+1))
	if err != nil {
		return err
	}
	if int64(len(raw)) > s.backend.service.config.MaxSize {
		return &smtp.SMTPError{Code: 552, Message: "Message size exceeds limit"}
	}
	now := time.Now().UTC()
	detail, headers, err := parseMessage(raw, now, s.from, nil)
	if err != nil {
		return &smtp.SMTPError{Code: 554, Message: "Invalid MIME message"}
	}
	parsed := &parsedMessage{detail: detail, headers: headers}
	command := s.conn.Command()
	if command != "BDAT" {
		command = "DATA"
	}
	all := &storedMessage{Raw: raw, Detail: withEnvelope(detail, s.recipients), Headers: headers, EnvelopeFrom: s.from, EnvelopeTo: s.recipients}
	if err := s.faults(command, "content", nil, all, true); err != nil {
		return err
	}
	for _, delivery := range s.deliveries {
		m := &storedMessage{Raw: raw, Detail: withEnvelope(detail, delivery.recipients), Headers: headers, EnvelopeFrom: s.from, EnvelopeTo: delivery.recipients}
		if err := s.faults(command, "content", delivery.account, m, false); err != nil {
			return err
		}
	}
	// SMTP has one final DATA result: reject before any delivery on content faults.
	// Account databases commit independently. A storage error can leave earlier
	// copies committed; the 451 retry may deliver duplicates to those accounts.
	for _, delivery := range s.deliveries {
		a := delivery.account
		if _, err := a.append(raw, appendOptions{Folder: s.backend.service.config.SMTPFolder, Date: now, From: s.from, To: delivery.recipients, Username: s.username, Notify: true, Parsed: parsed}); err != nil {
			a.rejected.Add(1)
			return &smtp.SMTPError{Code: 451, Message: "Message could not be stored"}
		}
		a.accepted.Add(1)
		a.acceptedSize.Add(uint64(len(raw)))
	}
	// Keep the parsed message through the completion hook; after faults model lost ACKs.
	s.completed = all
	s.conn.HookData = smtpCommandContext{username: s.username, accounts: append([]*smtpDelivery{}, s.deliveries...), message: all}
	return nil
}

func newSMTPServer(service *Service) (*smtp.Server, error) {
	c := service.config
	back := &smtpBackend{service: service}
	srv := smtp.NewServer(back)
	back.installFaultHooks(srv)
	srv.Addr = c.SMTPAddress
	srv.Domain = "mailarky.test"
	srv.MaxMessageBytes = c.MaxSize
	srv.MaxRecipients = 1000
	srv.ReadTimeout = time.Minute
	srv.WriteTimeout = time.Minute
	srv.AllowInsecureAuth = c.SMTPAllowInsecureAuth
	srv.EnableSMTPUTF8 = true
	if c.SMTPTLS != "" {
		certPath, keyPath := c.SMTPCert, c.SMTPKey
		if certPath == "" {
			certPath = c.Cert
			keyPath = c.Key
		}
		cert, err := tls.LoadX509KeyPair(certPath, keyPath)
		if err != nil {
			return nil, fmt.Errorf("SMTP certificate: %w", err)
		}
		srv.TLSConfig = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	}
	return srv, nil
}
