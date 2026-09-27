package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const maxMessageBytes = 50 << 20

type configuration struct {
	SMTPAddress           string        `yaml:"smtp"`
	IMAPAddress           string        `yaml:"imap"`
	HTTPAddress           string        `yaml:"http"`
	Cert                  string        `yaml:"cert"`
	Key                   string        `yaml:"key"`
	SMTPTLS               string        `yaml:"smtp_tls"`
	SMTPCert              string        `yaml:"smtp_cert"`
	SMTPKey               string        `yaml:"smtp_key"`
	RequireTLS            bool          `yaml:"require_tls"`
	ImplicitTLS           bool          `yaml:"smtp_require_tls"`
	SMTPAuth              string        `yaml:"smtp_auth"`
	HTTPAuth              string        `yaml:"http_auth"`
	SendAuth              string        `yaml:"send_auth"`
	SMTPAuthFile          string        `yaml:"smtp_auth_file"`
	SMTPAcceptAny         bool          `yaml:"smtp_accept_any"`
	SMTPAllowInsecureAuth bool          `yaml:"smtp_allow_insecure_auth"`
	HTTPAuthFile          string        `yaml:"http_auth_file"`
	HTTPCert              string        `yaml:"http_cert"`
	HTTPKey               string        `yaml:"http_key"`
	SendAuthFile          string        `yaml:"send_auth_file"`
	SendAcceptAny         bool          `yaml:"send_accept_any"`
	Username              string        `yaml:"imap_username"`
	Password              string        `yaml:"imap_password"`
	SMTPFolder            string        `yaml:"smtp_folder"`
	Database              string        `yaml:"database"`
	MaxMessages           int           `yaml:"max_messages"`
	MaxAge                time.Duration `yaml:"max_age"`
	MaxSize               int64         `yaml:"max_message_bytes"`
	IgnoreDuplicates      bool          `yaml:"ignore_duplicate_ids"`
	EnableChaos           bool          `yaml:"enable_chaos"`
	ChaosTriggers         string        `yaml:"chaos_triggers"`
	WebhookURL            string        `yaml:"webhook_url"`
	WebhookDelay          time.Duration `yaml:"webhook_delay"`
	WebhookLimit          time.Duration `yaml:"webhook_interval"`
	Label                 string        `yaml:"label"`
	TagsDisable           string        `yaml:"tags_disable"`
	TagsTitleCase         bool          `yaml:"tags_title_case"`
	TagsUsername          bool          `yaml:"tags_username"`
	TagsConfig            string        `yaml:"tags_config"`
	TagExpressions        string        `yaml:"tag"`
	TagFilters            []tagFilter   `yaml:"filters"`
	Relay                 relayConfig   `yaml:"relay"`
	Forward               relayConfig   `yaml:"forward"`
	RelayAll              bool          `yaml:"relay_all"`
	RelayMatching         string        `yaml:"relay_matching"`
	SpamAssassin          string        `yaml:"spamassassin"`
	AllowInternalHTTP     bool          `yaml:"allow_internal_http_requests"`
	BlockRemoteCSS        bool          `yaml:"block_remote_css_and_fonts"`
	EnableMetrics         bool          `yaml:"enable_prometheus"`
	Webroot               string        `yaml:"webroot"`
	CORS                  string        `yaml:"api_cors"`
	AllowedHosts          string        `yaml:"allowed_hosts"`
	DumpPath              string        `yaml:"dump_path"`
}

type tagFilter struct {
	Match string `yaml:"match"`
	Tags  string `yaml:"tags"`
}

func defaultConfig() configuration {
	return configuration{SMTPAddress: ":1025", IMAPAddress: ":1993", HTTPAddress: ":8026", Cert: "/certs/server.crt", Key: "/certs/server.key", Username: mailboxUsername, Password: mailboxPassword, SMTPFolder: "Sent", MaxMessages: 500, MaxSize: maxMessageBytes, WebhookLimit: time.Second}
}

func loadConfig(args []string) (configuration, error) {
	c := defaultConfig()
	if name := os.Getenv("MAIL_EMULATOR_CONFIG"); name != "" {
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
	fs := flag.NewFlagSet("imap-emulator", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var errs []error
	str := func(p *string, name string, envs ...string) {
		for _, e := range envs {
			if v, ok := os.LookupEnv(e); ok {
				*p = v
				break
			}
		}
		fs.StringVar(p, name, *p, name)
	}
	boolean := func(p *bool, name string, envs ...string) {
		for _, e := range envs {
			if v, ok := os.LookupEnv(e); ok {
				var err error
				*p, err = strconv.ParseBool(v)
				if err != nil {
					errs = append(errs, fmt.Errorf("%s: %w", e, err))
				}
				break
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
	seconds := func(p *time.Duration, name, env string) {
		if v, ok := os.LookupEnv(env); ok {
			n, err := strconv.Atoi(v)
			if err != nil || n < 0 {
				errs = append(errs, fmt.Errorf("%s must be nonnegative seconds", env))
			} else {
				*p = time.Duration(n) * time.Second
			}
		}
		fs.DurationVar(p, name, *p, name)
	}
	for _, pair := range []struct {
		p   *string
		env string
	}{{&c.SMTPAddress, "SMTP_EMULATOR_PORT"}, {&c.IMAPAddress, "IMAP_EMULATOR_PORT"}, {&c.HTTPAddress, "IMAP_EMULATOR_HTTP_PORT"}} {
		if v := os.Getenv(pair.env); v != "" {
			*pair.p = ":" + v
		}
	}
	str(&c.SMTPAddress, "smtp", "MP_SMTP_BIND_ADDR")
	str(&c.IMAPAddress, "imap", "IMAP_EMULATOR_BIND_ADDR")
	str(&c.HTTPAddress, "listen", "MP_UI_BIND_ADDR")
	str(&c.Cert, "imap-tls-cert", "IMAP_EMULATOR_CERT")
	str(&c.Key, "imap-tls-key", "IMAP_EMULATOR_KEY")
	str(&c.Username, "imap-username", "IMAP_EMULATOR_USERNAME")
	str(&c.Password, "imap-password", "IMAP_EMULATOR_PASSWORD")
	str(&c.SMTPFolder, "smtp-folder", "SMTP_EMULATOR_FOLDER")
	str(&c.SMTPTLS, "smtp-tls-mode", "SMTP_EMULATOR_TLS_MODE")
	str(&c.SMTPCert, "smtp-tls-cert", "MP_SMTP_TLS_CERT")
	str(&c.SMTPKey, "smtp-tls-key", "MP_SMTP_TLS_KEY")
	boolean(&c.RequireTLS, "smtp-require-starttls", "MP_SMTP_REQUIRE_STARTTLS")
	boolean(&c.ImplicitTLS, "smtp-require-tls", "MP_SMTP_REQUIRE_TLS")
	for env, pointer := range map[string]*string{"MP_SMTP_AUTH": &c.SMTPAuth, "MP_UI_AUTH": &c.HTTPAuth, "MP_SEND_API_AUTH": &c.SendAuth} {
		if value, ok := os.LookupEnv(env); ok {
			*pointer = value
		}
	}
	str(&c.SMTPAuthFile, "smtp-auth-file", "MP_SMTP_AUTH_FILE")
	boolean(&c.SMTPAcceptAny, "smtp-auth-accept-any", "MP_SMTP_AUTH_ACCEPT_ANY")
	boolean(&c.SMTPAllowInsecureAuth, "smtp-auth-allow-insecure", "MP_SMTP_AUTH_ALLOW_INSECURE")
	str(&c.HTTPAuthFile, "ui-auth-file", "MP_UI_AUTH_FILE")
	str(&c.HTTPCert, "ui-tls-cert", "MP_UI_TLS_CERT")
	str(&c.HTTPKey, "ui-tls-key", "MP_UI_TLS_KEY")
	str(&c.SendAuthFile, "send-api-auth-file", "MP_SEND_API_AUTH_FILE")
	boolean(&c.SendAcceptAny, "send-api-auth-accept-any", "MP_SEND_API_AUTH_ACCEPT_ANY")
	str(&c.Database, "database", "MP_DATABASE")
	integer(&c.MaxMessages, "max", "MP_MAX_MESSAGES")
	if v := os.Getenv("MP_MAX_AGE"); v != "" {
		var err error
		c.MaxAge, err = time.ParseDuration(v)
		if err != nil {
			errs = append(errs, err)
		}
	}
	fs.DurationVar(&c.MaxAge, "max-age", c.MaxAge, "retention age")
	sizeMB := int(c.MaxSize >> 20)
	integer(&sizeMB, "max-message-size", "MP_MAX_MESSAGE_SIZE")
	boolean(&c.IgnoreDuplicates, "ignore-duplicate-ids", "MP_IGNORE_DUPLICATE_IDS")
	boolean(&c.EnableChaos, "enable-chaos", "MP_ENABLE_CHAOS")
	str(&c.ChaosTriggers, "chaos-triggers", "MP_CHAOS_TRIGGERS")
	str(&c.WebhookURL, "webhook-url", "MP_WEBHOOK_URL")
	seconds(&c.WebhookDelay, "webhook-delay", "MP_WEBHOOK_DELAY")
	seconds(&c.WebhookLimit, "webhook-limit", "MP_WEBHOOK_LIMIT")
	str(&c.Label, "label", "MP_LABEL")
	str(&c.TagsDisable, "tags-disable", "MP_TAGS_DISABLE")
	boolean(&c.TagsTitleCase, "tags-title-case", "MP_TAGS_TITLE_CASE")
	boolean(&c.TagsUsername, "tags-username", "MP_TAGS_USERNAME")
	str(&c.TagsConfig, "tags-config", "MP_TAGS_CONFIG")
	str(&c.TagExpressions, "tag", "MP_TAG")
	boolean(&c.RelayAll, "smtp-relay-all", "MP_SMTP_RELAY_ALL")
	str(&c.RelayMatching, "smtp-relay-matching", "MP_SMTP_RELAY_MATCHING")
	str(&c.SpamAssassin, "spamassassin", "MP_SPAMASSASSIN")
	boolean(&c.AllowInternalHTTP, "allow-internal-http-requests", "MP_ALLOW_INTERNAL_HTTP_REQUESTS")
	boolean(&c.BlockRemoteCSS, "block-remote-css-and-fonts", "MP_BLOCK_REMOTE_CSS_AND_FONTS")
	boolean(&c.EnableMetrics, "enable-prometheus", "MP_ENABLE_PROMETHEUS")
	str(&c.Webroot, "webroot", "MP_WEBROOT")
	str(&c.CORS, "api-cors", "MP_API_CORS")
	str(&c.AllowedHosts, "allowed-hosts", "MP_ALLOWED_HOSTS")
	str(&c.DumpPath, "dump-path", "MP_DUMP_PATH")
	if err := fs.Parse(args); err != nil {
		return c, err
	}
	if fs.NArg() != 0 {
		return c, fmt.Errorf("unexpected arguments: %v", fs.Args())
	}
	_, sizeSet := os.LookupEnv("MP_MAX_MESSAGE_SIZE")
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
	if c.Username == "" || c.Password == "" {
		return c, errors.New("IMAP credentials must not be empty")
	}
	if c.EnableChaos || c.ChaosTriggers != "" {
		return c, errors.New("global chaos is unsupported; use message-scoped /api/v1/toxics")
	}
	if (c.SMTPCert == "") != (c.SMTPKey == "") || (c.HTTPCert == "") != (c.HTTPKey == "") {
		return c, errors.New("TLS certificates and keys must be configured together")
	}
	if c.WebhookDelay < 0 || c.WebhookLimit < 0 {
		return c, errors.New("webhook delays must be nonnegative")
	}
	if c.RelayMatching != "" {
		if _, err := regexp.Compile(c.RelayMatching); err != nil {
			return c, fmt.Errorf("relay matching: %w", err)
		}
	}
	if c.SMTPTLS == "" && c.SMTPCert != "" {
		c.SMTPTLS = "starttls"
	}
	if c.ImplicitTLS {
		if c.RequireTLS {
			return c, errors.New("implicit TLS and required STARTTLS are mutually exclusive")
		}
		c.SMTPTLS = "tls"
	}
	if c.SMTPTLS != "" && c.SMTPTLS != "starttls" && c.SMTPTLS != "tls" {
		return c, errors.New("smtp-tls-mode must be starttls or tls")
	}
	if c.RequireTLS && c.SMTPTLS == "" {
		return c, errors.New("smtp-require-tls needs SMTP TLS configuration")
	}
	for _, address := range []string{c.SMTPAddress, c.IMAPAddress, c.HTTPAddress} {
		if _, _, err := net.SplitHostPort(address); err != nil {
			return c, fmt.Errorf("invalid listener %q: %w", address, err)
		}
	}
	if c.RelayAll && c.RelayMatching != "" {
		return c, errors.New("relay-all and relay-matching are mutually exclusive")
	}
	if err := loadRelayConfig(&c.Relay, "MP_SMTP_RELAY_"); err != nil {
		return c, err
	}
	if err := loadRelayConfig(&c.Forward, "MP_SMTP_FORWARD_"); err != nil {
		return c, err
	}
	if c.TagsConfig != "" {
		data, err := os.ReadFile(c.TagsConfig)
		if err != nil {
			return c, err
		}
		var tags struct {
			Filters []tagFilter `yaml:"filters"`
		}
		if err := yaml.Unmarshal(data, &tags); err != nil {
			return c, err
		}
		c.TagFilters = append(c.TagFilters, tags.Filters...)
	}
	expressions, err := splitExpressions(c.TagExpressions)
	if err != nil {
		return c, err
	}
	for _, expression := range expressions {
		tags, match, ok := strings.Cut(expression, "=")
		if !ok || match == "" {
			return c, errors.New("tag expressions must use tag=match")
		}
		c.TagFilters = append(c.TagFilters, tagFilter{Tags: tags, Match: match})
	}
	for _, filter := range c.TagFilters {
		if _, err := normalizeTags(strings.Split(filter.Tags, ",")); err != nil {
			return c, err
		}
		if _, err := compileTagSearch(filter.Match); err != nil {
			return c, err
		}
	}
	return c, nil
}
