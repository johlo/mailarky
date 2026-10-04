package sandbox

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const maxMessageBytes = 50 << 20
const defaultAccountID = "default"

type configuration struct {
	SMTPAddress           string        `yaml:"smtp"`
	IMAPAddress           string        `yaml:"imap"`
	HTTPAddress           string        `yaml:"http"`
	Cert                  string        `yaml:"imap_cert"`
	Key                   string        `yaml:"imap_key"`
	SMTPTLS               string        `yaml:"smtp_tls_mode"`
	SMTPCert              string        `yaml:"smtp_cert"`
	SMTPKey               string        `yaml:"smtp_key"`
	RequireTLS            bool          `yaml:"smtp_require_starttls"`
	SMTPRequireAuth       bool          `yaml:"smtp_require_auth"`
	SMTPAllowInsecureAuth bool          `yaml:"smtp_allow_insecure_auth"`
	HTTPAuth              string        `yaml:"http_auth"`
	HTTPAuthFile          string        `yaml:"http_auth_file"`
	HTTPCert              string        `yaml:"http_cert"`
	HTTPKey               string        `yaml:"http_key"`
	Username              string        `yaml:"username"`
	Password              string        `yaml:"password"`
	SMTPFolder            string        `yaml:"smtp_folder"`
	Database              string        `yaml:"database"`
	MaxMessages           int           `yaml:"max_messages"`
	MaxAge                time.Duration `yaml:"max_age"`
	MaxSize               int64         `yaml:"max_message_bytes"`
	IgnoreDuplicates      bool          `yaml:"ignore_duplicate_ids"`
	WebhookURL            string        `yaml:"webhook_url"`
	WebhookDelay          time.Duration `yaml:"webhook_delay"`
	WebhookLimit          time.Duration `yaml:"webhook_interval"`
	Label                 string        `yaml:"label"`
	EnableMetrics         bool          `yaml:"enable_metrics"`
	Webroot               string        `yaml:"webroot"`
	DumpPath              string        `yaml:"dump_path"`
}

func defaultConfig() configuration {
	return configuration{SMTPAddress: ":1025", IMAPAddress: ":1993", HTTPAddress: ":8026", Cert: "/certs/server.crt", Key: "/certs/server.key", Username: mailboxUsername, Password: mailboxPassword, SMTPFolder: "Sent", MaxSize: maxMessageBytes, WebhookLimit: time.Second}
}

func loadConfig(args []string) (configuration, error) {
	c := defaultConfig()
	if name := os.Getenv("MAILARKY_CONFIG"); name != "" {
		data, err := os.ReadFile(name)
		if err != nil {
			return c, err
		}
		decoder := yaml.NewDecoder(strings.NewReader(string(data)))
		decoder.KnownFields(true)
		if err := decoder.Decode(&c); err != nil {
			return c, err
		}
	}
	fs := flag.NewFlagSet("mailarky", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var errs []error
	str := func(p *string, name, env string) {
		if v, ok := os.LookupEnv(env); ok {
			*p = v
		}
		fs.StringVar(p, name, *p, name)
	}
	boolean := func(p *bool, name, env string) {
		if v, ok := os.LookupEnv(env); ok {
			var err error
			*p, err = strconv.ParseBool(v)
			if err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", env, err))
			}
		}
		fs.BoolVar(p, name, *p, name)
	}
	integer := func(p *int, name, env string) {
		if v, ok := os.LookupEnv(env); ok {
			var err error
			*p, err = strconv.Atoi(v)
			if err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", env, err))
			}
		}
		fs.IntVar(p, name, *p, name)
	}
	duration := func(p *time.Duration, name, env string) {
		if v, ok := os.LookupEnv(env); ok {
			var err error
			*p, err = time.ParseDuration(v)
			if err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", env, err))
			}
		}
		fs.DurationVar(p, name, *p, name)
	}
	for _, pair := range []struct {
		p   *string
		env string
	}{{&c.SMTPAddress, "MAILARKY_SMTP_PORT"}, {&c.IMAPAddress, "MAILARKY_IMAP_PORT"}, {&c.HTTPAddress, "MAILARKY_HTTP_PORT"}} {
		if v := os.Getenv(pair.env); v != "" {
			*pair.p = ":" + v
		}
	}
	str(&c.SMTPAddress, "smtp", "MAILARKY_SMTP_BIND_ADDR")
	str(&c.IMAPAddress, "imap", "MAILARKY_IMAP_BIND_ADDR")
	str(&c.HTTPAddress, "http", "MAILARKY_HTTP_BIND_ADDR")
	str(&c.Cert, "imap-tls-cert", "MAILARKY_IMAP_CERT")
	str(&c.Key, "imap-tls-key", "MAILARKY_IMAP_KEY")
	str(&c.Username, "username", "MAILARKY_USERNAME")
	str(&c.Password, "password", "MAILARKY_PASSWORD")
	str(&c.SMTPFolder, "smtp-folder", "MAILARKY_SMTP_FOLDER")
	str(&c.SMTPTLS, "smtp-tls-mode", "MAILARKY_SMTP_TLS_MODE")
	str(&c.SMTPCert, "smtp-tls-cert", "MAILARKY_SMTP_TLS_CERT")
	str(&c.SMTPKey, "smtp-tls-key", "MAILARKY_SMTP_TLS_KEY")
	boolean(&c.RequireTLS, "smtp-require-starttls", "MAILARKY_SMTP_REQUIRE_STARTTLS")
	boolean(&c.SMTPRequireAuth, "smtp-require-auth", "MAILARKY_SMTP_REQUIRE_AUTH")
	boolean(&c.SMTPAllowInsecureAuth, "smtp-auth-allow-insecure", "MAILARKY_SMTP_AUTH_ALLOW_INSECURE")
	str(&c.HTTPAuth, "http-auth", "MAILARKY_HTTP_AUTH")
	str(&c.HTTPAuthFile, "http-auth-file", "MAILARKY_HTTP_AUTH_FILE")
	str(&c.HTTPCert, "http-tls-cert", "MAILARKY_HTTP_TLS_CERT")
	str(&c.HTTPKey, "http-tls-key", "MAILARKY_HTTP_TLS_KEY")
	str(&c.Database, "database", "MAILARKY_DATABASE")
	integer(&c.MaxMessages, "max-messages", "MAILARKY_MAX_MESSAGES")
	duration(&c.MaxAge, "max-age", "MAILARKY_MAX_AGE")
	sizeMB := int(c.MaxSize >> 20)
	integer(&sizeMB, "max-message-size", "MAILARKY_MAX_MESSAGE_SIZE")
	boolean(&c.IgnoreDuplicates, "ignore-duplicate-ids", "MAILARKY_IGNORE_DUPLICATE_IDS")
	str(&c.WebhookURL, "webhook-url", "MAILARKY_WEBHOOK_URL")
	duration(&c.WebhookDelay, "webhook-delay", "MAILARKY_WEBHOOK_DELAY")
	duration(&c.WebhookLimit, "webhook-interval", "MAILARKY_WEBHOOK_INTERVAL")
	str(&c.Label, "label", "MAILARKY_LABEL")
	boolean(&c.EnableMetrics, "metrics", "MAILARKY_ENABLE_METRICS")
	str(&c.Webroot, "webroot", "MAILARKY_WEBROOT")
	str(&c.DumpPath, "dump-path", "MAILARKY_DUMP_PATH")
	if err := fs.Parse(args); err != nil {
		return c, err
	}
	if fs.NArg() != 0 {
		return c, fmt.Errorf("unexpected arguments: %v", fs.Args())
	}
	_, sizeSet := os.LookupEnv("MAILARKY_MAX_MESSAGE_SIZE")
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "max-message-size" {
			sizeSet = true
		}
	})
	if sizeSet {
		c.MaxSize = int64(sizeMB) << 20
	}
	if err := errors.Join(errs...); err != nil {
		return c, err
	}
	if c.MaxSize <= 0 || c.MaxMessages < 0 || c.MaxAge < 0 {
		return c, errors.New("message size must be positive and retention limits nonnegative")
	}
	if c.Username == "" || c.Password == "" || len(c.Password) > 72 {
		return c, errors.New("account username and password are required; password must be at most 72 bytes")
	}
	if (c.SMTPCert == "") != (c.SMTPKey == "") || (c.HTTPCert == "") != (c.HTTPKey == "") {
		return c, errors.New("TLS certificates and keys must be configured together")
	}
	if c.WebhookDelay < 0 || c.WebhookLimit < 0 {
		return c, errors.New("webhook durations must be nonnegative")
	}
	if c.SMTPTLS != "" && c.SMTPTLS != "starttls" && c.SMTPTLS != "tls" {
		return c, errors.New("smtp-tls-mode must be starttls or tls")
	}
	if c.RequireTLS && c.SMTPTLS != "starttls" {
		return c, errors.New("smtp-require-starttls requires smtp-tls-mode=starttls")
	}
	for _, address := range []string{c.SMTPAddress, c.IMAPAddress, c.HTTPAddress} {
		if _, _, err := net.SplitHostPort(address); err != nil {
			return c, fmt.Errorf("invalid listener %q: %w", address, err)
		}
	}
	c.Webroot = strings.TrimRight(c.Webroot, "/")
	if c.Webroot != "" && (!strings.HasPrefix(c.Webroot, "/") || strings.ContainsAny(c.Webroot, "?#{ }")) {
		return c, errors.New("webroot must be an absolute URL path")
	}
	return c, nil
}
