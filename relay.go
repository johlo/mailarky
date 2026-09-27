package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/md5"
	"crypto/tls"
	"errors"
	"fmt"
	"log"
	"net"
	"net/mail"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/emersion/go-sasl"
	smtp "github.com/emersion/go-smtp"
	"github.com/google/uuid"
	"gopkg.in/yaml.v3"
)

type relayConfig struct {
	Host               string `yaml:"host"`
	Port               int    `yaml:"port"`
	StartTLS           bool   `yaml:"starttls"`
	TLS                bool   `yaml:"tls"`
	AllowInsecure      bool   `yaml:"allow-insecure"`
	Auth               string `yaml:"auth"`
	Username           string `yaml:"username"`
	Password           string `yaml:"password"`
	Secret             string `yaml:"secret"`
	ReturnPath         string `yaml:"return-path"`
	OverrideFrom       string `yaml:"override-from"`
	AllowedRecipients  string `yaml:"allowed-recipients"`
	BlockedRecipients  string `yaml:"blocked-recipients"`
	PreserveMessageIDs bool   `yaml:"preserve-message-ids"`
	ForwardErrors      bool   `yaml:"forward-smtp-errors"`
	To                 string `yaml:"to"`
}

func loadRelayConfig(c *relayConfig, prefix string) error {
	if path := os.Getenv(prefix + "CONFIG"); path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		decoder := yaml.NewDecoder(bytes.NewReader(data))
		decoder.KnownFields(true)
		if err := decoder.Decode(c); err != nil {
			return err
		}
	}
	for key, pointer := range map[string]*string{"HOST": &c.Host, "AUTH": &c.Auth, "USERNAME": &c.Username, "PASSWORD": &c.Password, "SECRET": &c.Secret, "RETURN_PATH": &c.ReturnPath, "OVERRIDE_FROM": &c.OverrideFrom, "ALLOWED_RECIPIENTS": &c.AllowedRecipients, "BLOCKED_RECIPIENTS": &c.BlockedRecipients, "TO": &c.To} {
		if v, ok := os.LookupEnv(prefix + key); ok {
			*pointer = v
		}
	}
	for key, pointer := range map[string]*bool{"STARTTLS": &c.StartTLS, "TLS": &c.TLS, "ALLOW_INSECURE": &c.AllowInsecure, "PRESERVE_MESSAGE_IDS": &c.PreserveMessageIDs, "FWD_SMTP_ERRORS": &c.ForwardErrors} {
		if v, ok := os.LookupEnv(prefix + key); ok {
			parsed, err := strconv.ParseBool(v)
			if err != nil {
				return fmt.Errorf("%s%s: %w", prefix, key, err)
			}
			*pointer = parsed
		}
	}
	if v := os.Getenv(prefix + "PORT"); v != "" {
		port, err := strconv.Atoi(v)
		if err != nil {
			return err
		}
		c.Port = port
	}
	if c.Port == 0 {
		c.Port = 25
	}
	if c.Host == "" {
		return nil
	}
	if c.Port < 1 || c.Port > 65535 {
		return errors.New("invalid relay port")
	}
	if c.TLS && c.StartTLS {
		return errors.New("relay TLS and STARTTLS are mutually exclusive")
	}
	for _, pattern := range []string{c.AllowedRecipients, c.BlockedRecipients} {
		if pattern != "" {
			if _, err := regexp.Compile(pattern); err != nil {
				return err
			}
		}
	}
	for _, address := range []string{c.ReturnPath, c.OverrideFrom} {
		if address != "" && !validEnvelope(address, false) {
			return errors.New("relay return-path and override-from must be email addresses")
		}
	}
	if c.To != "" {
		for _, to := range strings.Split(c.To, ",") {
			if !validEnvelope(strings.TrimSpace(to), false) {
				return errors.New("invalid forward recipient")
			}
		}
	}
	switch c.Auth {
	case "", "none":
	case "plain", "login":
		if c.Username == "" || c.Password == "" {
			return errors.New("relay authentication requires username and password")
		}
	case "cram-md5":
		if c.Username == "" || c.Secret == "" {
			return errors.New("CRAM-MD5 requires username and secret")
		}
	default:
		return errors.New("unsupported relay authentication")
	}
	return nil
}

type cramClient struct{ username, secret string }

func (c cramClient) Start() (string, []byte, error) { return "CRAM-MD5", nil, nil }
func (c cramClient) Next(challenge []byte) ([]byte, error) {
	hash := hmac.New(md5.New, []byte(c.secret))
	hash.Write(challenge)
	return []byte(fmt.Sprintf("%s %x", c.username, hash.Sum(nil))), nil
}

func relayMessage(ctx context.Context, c relayConfig, m *storedMessage, recipients []string, manual bool) error {
	if c.Host == "" {
		return errors.New("SMTP relay is not configured")
	}
	if len(recipients) == 0 {
		return errors.New("relay recipients are required")
	}
	for _, to := range recipients {
		if !validEnvelope(to, false) {
			return errors.New("invalid relay recipient")
		}
		if c.BlockedRecipients != "" && regexp.MustCompile(c.BlockedRecipients).MatchString(to) {
			return errors.New("relay recipient is blocked")
		}
		if manual && c.AllowedRecipients != "" && !regexp.MustCompile(c.AllowedRecipients).MatchString(to) {
			return errors.New("relay recipient is not allowed")
		}
	}
	from := m.EnvelopeFrom
	if from == "" {
		from = m.Detail.From.Address
	}
	raw := m.Raw
	replace := map[string]string{}
	if c.ReturnPath != "" {
		from = c.ReturnPath
		replace["Return-Path"] = "<" + from + ">"
	}
	if c.OverrideFrom != "" {
		replace["X-Original-From"] = headerText(m.Headers, "From")
		replace["From"] = (&mail.Address{Name: m.Detail.From.Name, Address: c.OverrideFrom}).String()
		if c.ReturnPath == "" {
			from = c.OverrideFrom
		}
	}
	if manual && !c.PreserveMessageIDs {
		replace["Message-ID"] = "<" + uuid.NewString() + "@mail-emulator.test>"
	}
	if len(replace) > 0 {
		raw = replaceHeaders(raw, replace)
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", net.JoinHostPort(c.Host, strconv.Itoa(c.Port)))
	if err != nil {
		return err
	}
	defer conn.Close()
	deadline, _ := ctx.Deadline()
	conn.SetDeadline(deadline)
	tlsConfig := &tls.Config{ServerName: c.Host, MinVersion: tls.VersionTLS12, InsecureSkipVerify: c.AllowInsecure}
	var client *smtp.Client
	if c.TLS {
		secure := tls.Client(conn, tlsConfig)
		if err := secure.HandshakeContext(ctx); err != nil {
			return err
		}
		client = smtp.NewClient(secure)
	} else if c.StartTLS {
		client, err = smtp.NewClientStartTLS(conn, tlsConfig)
		if err != nil {
			return err
		}
	} else {
		client = smtp.NewClient(conn)
	}
	defer client.Close()
	var auth sasl.Client
	switch c.Auth {
	case "plain":
		auth = sasl.NewPlainClient("", c.Username, c.Password)
	case "login":
		auth = sasl.NewLoginClient(c.Username, c.Password)
	case "cram-md5":
		auth = cramClient{c.Username, c.Secret}
	}
	if auth != nil {
		if err := client.Auth(auth); err != nil {
			return err
		}
	}
	if err := client.SendMail(from, recipients, bytes.NewReader(raw)); err != nil {
		return err
	}
	return client.Quit()
}

func (b *mailboxBackend) deliver(ctx context.Context, m *storedMessage) error {
	c := b.config
	if c.Forward.Host != "" && c.Forward.To != "" {
		var recipients []string
		for _, to := range strings.Split(c.Forward.To, ",") {
			recipients = append(recipients, strings.TrimSpace(to))
		}
		if err := relayMessage(ctx, c.Forward, m, recipients, false); err != nil {
			if c.Forward.ForwardErrors {
				return err
			}
			log.Printf("forward message %s: %v", m.ID, err)
		}
	}
	if c.Relay.Host != "" && (c.RelayAll || c.RelayMatching != "") {
		var recipients []string
		for _, to := range m.EnvelopeTo {
			if c.Relay.BlockedRecipients != "" && regexp.MustCompile(c.Relay.BlockedRecipients).MatchString(to) {
				continue
			}
			if c.RelayAll || regexp.MustCompile(c.RelayMatching).MatchString(to) {
				recipients = append(recipients, to)
			}
		}
		if len(recipients) > 0 {
			if err := relayMessage(ctx, c.Relay, m, recipients, false); err != nil {
				if c.Relay.ForwardErrors {
					return err
				}
				log.Printf("relay message %s: %v", m.ID, err)
			}
		}
	}
	return nil
}
