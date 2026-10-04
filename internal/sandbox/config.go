package sandbox

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
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

// Register flags before loading settings so help always shows built-in defaults.
func configFlags(c *configuration, sizeMB *int, output io.Writer) (*flag.FlagSet, map[string]string) {
	fs := flag.NewFlagSet("mailarky", flag.ContinueOnError)
	fs.SetOutput(output)
	fs.Usage = func() {
		fmt.Fprintln(output, "Mailarky: SMTP and IMAP test server with isolated accounts and programmable failures.")
		fmt.Fprintln(output, "\nUsage:\n  mailarky [flags]\n  mailarky sendmail [flags] [recipient ...]\n  mailarky --version\n  mailarky --help")
		fmt.Fprintln(output, "\nServer flags (built-in defaults):")
		fs.PrintDefaults()
		fmt.Fprintln(output, "\nConfiguration precedence: defaults < MAILARKY_CONFIG YAML < environment < flags.")
		fmt.Fprintln(output, "MAILARKY_SMTP_PORT, MAILARKY_IMAP_PORT and MAILARKY_HTTP_PORT set listener ports;")
		fmt.Fprintln(output, "the corresponding MAILARKY_*_BIND_ADDR variables override them.")
		fmt.Fprintln(output, "\nIMAP requires a TLS certificate and key. Outside Docker, set --imap-tls-cert and --imap-tls-key.")
		fmt.Fprintln(output, "Setup: https://github.com/johlo/mailarky/blob/main/docs/how-to/run-and-test.md#run-without-docker")
	}
	envs := make(map[string]string)
	str := func(p *string, name, env, description string) {
		fs.StringVar(p, name, *p, description+" ("+env+")")
		envs[env] = name
	}
	boolean := func(p *bool, name, env, description string) {
		fs.BoolVar(p, name, *p, description+" ("+env+")")
		envs[env] = name
	}
	integer := func(p *int, name, env, description string) {
		fs.IntVar(p, name, *p, description+" ("+env+")")
		envs[env] = name
	}
	duration := func(p *time.Duration, name, env, description string) {
		fs.DurationVar(p, name, *p, description+" ("+env+")")
		envs[env] = name
	}
	str(&c.SMTPAddress, "smtp", "MAILARKY_SMTP_BIND_ADDR", "SMTP listen address")
	str(&c.IMAPAddress, "imap", "MAILARKY_IMAP_BIND_ADDR", "IMAP TLS listen address")
	str(&c.HTTPAddress, "http", "MAILARKY_HTTP_BIND_ADDR", "HTTP control API listen address")
	str(&c.Cert, "imap-tls-cert", "MAILARKY_IMAP_CERT", "IMAP PEM certificate file")
	str(&c.Key, "imap-tls-key", "MAILARKY_IMAP_KEY", "IMAP PEM private key file")
	str(&c.Username, "username", "MAILARKY_USERNAME", "Default account username")
	str(&c.Password, "password", "MAILARKY_PASSWORD", "Default account password")
	str(&c.SMTPFolder, "smtp-folder", "MAILARKY_SMTP_FOLDER", "Folder for captured SMTP messages")
	str(&c.SMTPTLS, "smtp-tls-mode", "MAILARKY_SMTP_TLS_MODE", "SMTP TLS mode: starttls, tls, or empty for plaintext")
	str(&c.SMTPCert, "smtp-tls-cert", "MAILARKY_SMTP_TLS_CERT", "SMTP certificate file (defaults to IMAP certificate)")
	str(&c.SMTPKey, "smtp-tls-key", "MAILARKY_SMTP_TLS_KEY", "SMTP private key file (defaults to IMAP key)")
	boolean(&c.RequireTLS, "smtp-require-starttls", "MAILARKY_SMTP_REQUIRE_STARTTLS", "Require STARTTLS before SMTP delivery")
	boolean(&c.SMTPRequireAuth, "smtp-require-auth", "MAILARKY_SMTP_REQUIRE_AUTH", "Require SMTP authentication")
	boolean(&c.SMTPAllowInsecureAuth, "smtp-auth-allow-insecure", "MAILARKY_SMTP_AUTH_ALLOW_INSECURE", "Allow SMTP authentication without TLS")
	str(&c.HTTPAuth, "http-auth", "MAILARKY_HTTP_AUTH", "HTTP credentials as whitespace-separated username:password pairs")
	str(&c.HTTPAuthFile, "http-auth-file", "MAILARKY_HTTP_AUTH_FILE", "File containing HTTP credentials")
	str(&c.HTTPCert, "http-tls-cert", "MAILARKY_HTTP_TLS_CERT", "HTTPS certificate file")
	str(&c.HTTPKey, "http-tls-key", "MAILARKY_HTTP_TLS_KEY", "HTTPS private key file")
	str(&c.Database, "database", "MAILARKY_DATABASE", "Persistence file (empty for in-memory storage)")
	integer(&c.MaxMessages, "max-messages", "MAILARKY_MAX_MESSAGES", "Maximum messages per account (0 for unlimited)")
	duration(&c.MaxAge, "max-age", "MAILARKY_MAX_AGE", "Maximum message age, e.g. 24h (0 for unlimited)")
	integer(sizeMB, "max-message-size", "MAILARKY_MAX_MESSAGE_SIZE", "Maximum message size in MiB")
	boolean(&c.IgnoreDuplicates, "ignore-duplicate-ids", "MAILARKY_IGNORE_DUPLICATE_IDS", "Ignore duplicate Message-ID values within an account")
	str(&c.WebhookURL, "webhook-url", "MAILARKY_WEBHOOK_URL", "Message notification webhook URL")
	duration(&c.WebhookDelay, "webhook-delay", "MAILARKY_WEBHOOK_DELAY", "Delay before webhook delivery")
	duration(&c.WebhookLimit, "webhook-interval", "MAILARKY_WEBHOOK_INTERVAL", "Minimum interval between webhook deliveries")
	str(&c.Label, "label", "MAILARKY_LABEL", "Mailarky-Label webhook header value")
	boolean(&c.EnableMetrics, "metrics", "MAILARKY_ENABLE_METRICS", "Enable the metrics endpoint")
	str(&c.Webroot, "webroot", "MAILARKY_WEBROOT", "HTTP URL path prefix")
	str(&c.DumpPath, "dump-path", "MAILARKY_DUMP_PATH", "Directory for raw .eml message dumps")
	return fs, envs
}

func printUsage(output io.Writer) {
	c := defaultConfig()
	sizeMB := int(c.MaxSize >> 20)
	fs, _ := configFlags(&c, &sizeMB, output)
	fs.Usage()
}

func loadConfig(args []string, output io.Writer) (configuration, error) {
	c := defaultConfig()
	sizeMB := int(c.MaxSize >> 20)
	fs, envs := configFlags(&c, &sizeMB, output)
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
	sizeMB = int(c.MaxSize >> 20)
	for _, pair := range []struct {
		p   *string
		env string
	}{{&c.SMTPAddress, "MAILARKY_SMTP_PORT"}, {&c.IMAPAddress, "MAILARKY_IMAP_PORT"}, {&c.HTTPAddress, "MAILARKY_HTTP_PORT"}} {
		if v := os.Getenv(pair.env); v != "" {
			*pair.p = ":" + v
		}
	}
	var errs []error
	for env, name := range envs {
		if value, ok := os.LookupEnv(env); ok {
			if err := fs.Set(name, value); err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", env, err))
			}
		}
	}
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
