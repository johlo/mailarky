package sandbox

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestEnvironmentAndFlagPrecedence(t *testing.T) {
	configFile := filepath.Join(t.TempDir(), "mailarky.yaml")
	if err := os.WriteFile(configFile, []byte("max_messages: 7\nusername: yaml-user\n"), 0600); err != nil {
		t.Fatal(err)
	}
	settings := map[string]string{
		"MAILARKY_CONFIG":        configFile,
		"MAILARKY_SMTP_PORT":     "21025",
		"MAILARKY_IMAP_PORT":     "21993",
		"MAILARKY_HTTP_PORT":     "28026",
		"MAILARKY_USERNAME":      "env-user",
		"MAILARKY_PASSWORD":      "env-password",
		"MAILARKY_IMAP_CERT":     "test.crt",
		"MAILARKY_IMAP_KEY":      "test.key",
		"MAILARKY_SMTP_FOLDER":   "Archive",
		"MAILARKY_SMTP_TLS_MODE": "starttls",
		"MAILARKY_MAX_MESSAGES":  "9",
	}
	for name, value := range settings {
		t.Setenv(name, value)
	}
	c, err := loadConfig(nil)
	if err != nil {
		t.Fatal(err)
	}
	if c.MaxMessages != 9 || c.SMTPAddress != ":21025" || c.IMAPAddress != ":21993" || c.HTTPAddress != ":28026" ||
		c.Username != "env-user" || c.Password != "env-password" || c.Cert != "test.crt" || c.Key != "test.key" || c.SMTPFolder != "Archive" || c.SMTPTLS != "starttls" {
		t.Fatal("environment did not configure the service or override YAML")
	}
	t.Setenv("MAILARKY_SMTP_BIND_ADDR", "127.0.0.1:31025")
	t.Setenv("MAILARKY_IMAP_BIND_ADDR", "127.0.0.1:31993")
	t.Setenv("MAILARKY_HTTP_BIND_ADDR", "127.0.0.1:38026")
	c, err = loadConfig(nil)
	if err != nil || c.SMTPAddress != "127.0.0.1:31025" || c.IMAPAddress != "127.0.0.1:31993" || c.HTTPAddress != "127.0.0.1:38026" {
		t.Fatal("bind addresses did not override port settings", err)
	}
	c, err = loadConfig([]string{"--imap=127.0.0.1:41993", "--max-messages=11", "--username=cli-user"})
	if err != nil || c.IMAPAddress != "127.0.0.1:41993" || c.MaxMessages != 11 || c.Username != "cli-user" {
		t.Fatal("command-line flags did not override environment and YAML", err)
	}
}

func TestDurationSettingsAndUnambiguousTLSModes(t *testing.T) {
	t.Setenv("MAILARKY_MAX_AGE", "2h")
	t.Setenv("MAILARKY_WEBHOOK_DELAY", "250ms")
	t.Setenv("MAILARKY_WEBHOOK_INTERVAL", "1.5s")
	c, err := loadConfig(nil)
	if err != nil || c.MaxAge != 2*time.Hour || c.WebhookDelay != 250*time.Millisecond || c.WebhookLimit != 1500*time.Millisecond {
		t.Fatal(c, err)
	}
	if c.MaxMessages != 0 {
		t.Fatal("retention should be unlimited by default")
	}
	t.Setenv("MAILARKY_SMTP_REQUIRE_STARTTLS", "true")
	if _, err := loadConfig(nil); err == nil || !strings.Contains(err.Error(), "smtp-require-starttls") {
		t.Fatal(err)
	}
	t.Setenv("MAILARKY_SMTP_TLS_MODE", "starttls")
	if _, err := loadConfig(nil); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MAILARKY_SMTP_REQUIRE_STARTTLS", "false")
	t.Setenv("MAILARKY_SMTP_TLS_MODE", "tls")
	if c, err := loadConfig(nil); err != nil || c.SMTPTLS != "tls" {
		t.Fatal(c, err)
	}
	if _, err := loadConfig([]string{"--enable-chaos"}); err == nil {
		t.Fatal("removed flag accepted")
	}
}
