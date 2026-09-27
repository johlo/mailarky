package sandbox

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRenameKeepsEnvironmentAliasesAndPrecedence(t *testing.T) {
	legacyFile := filepath.Join(t.TempDir(), "legacy.yaml")
	currentFile := filepath.Join(t.TempDir(), "current.yaml")
	for path, content := range map[string]string{legacyFile: "max_messages: 7\n", currentFile: "max_messages: 9\n"} {
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	settings := []struct{ old, current, oldValue, currentValue string }{
		{"MAIL_EMULATOR_CONFIG", "MAIL_SANDBOX_CONFIG", legacyFile, currentFile},
		{"SMTP_EMULATOR_PORT", "MAIL_SANDBOX_SMTP_PORT", "11025", "21025"},
		{"IMAP_EMULATOR_PORT", "MAIL_SANDBOX_IMAP_PORT", "11993", "21993"},
		{"IMAP_EMULATOR_HTTP_PORT", "MAIL_SANDBOX_HTTP_PORT", "18026", "28026"},
		{"IMAP_EMULATOR_USERNAME", "MAIL_SANDBOX_IMAP_USERNAME", "legacy-user", "current-user"},
		{"IMAP_EMULATOR_PASSWORD", "MAIL_SANDBOX_IMAP_PASSWORD", "legacy-password", "current-password"},
		{"IMAP_EMULATOR_CERT", "MAIL_SANDBOX_IMAP_CERT", "legacy.crt", "current.crt"},
		{"IMAP_EMULATOR_KEY", "MAIL_SANDBOX_IMAP_KEY", "legacy.key", "current.key"},
		{"SMTP_EMULATOR_FOLDER", "MAIL_SANDBOX_SMTP_FOLDER", "INBOX", "Archive"},
		{"SMTP_EMULATOR_TLS_MODE", "MAIL_SANDBOX_SMTP_TLS_MODE", "tls", "starttls"},
	}
	for _, setting := range settings {
		t.Setenv(setting.old, setting.oldValue)
		t.Setenv(setting.current, setting.currentValue)
	}
	c, err := loadConfig(nil)
	if err != nil {
		t.Fatal(err)
	}
	if c.MaxMessages != 9 || c.SMTPAddress != ":21025" || c.IMAPAddress != ":21993" || c.HTTPAddress != ":28026" ||
		c.Username != "current-user" || c.Password != "current-password" || c.Cert != "current.crt" || c.Key != "current.key" || c.SMTPFolder != "Archive" || c.SMTPTLS != "starttls" {
		t.Fatal("new names did not take precedence over aliases")
	}
	for _, setting := range settings {
		if err := os.Unsetenv(setting.current); err != nil {
			t.Fatal(err)
		}
	}
	c, err = loadConfig(nil)
	if err != nil {
		t.Fatal(err)
	}
	if c.MaxMessages != 7 || c.SMTPAddress != ":11025" || c.IMAPAddress != ":11993" || c.HTTPAddress != ":18026" ||
		c.Username != "legacy-user" || c.Password != "legacy-password" || c.Cert != "legacy.crt" || c.Key != "legacy.key" || c.SMTPFolder != "INBOX" || c.SMTPTLS != "tls" {
		t.Fatal("legacy environment no longer configures the service")
	}
	t.Setenv("IMAP_EMULATOR_BIND_ADDR", "127.0.0.1:11994")
	c, err = loadConfig(nil)
	if err != nil || c.IMAPAddress != "127.0.0.1:11994" {
		t.Fatal("legacy bind address did not override port", err)
	}
	t.Setenv("MAIL_SANDBOX_IMAP_BIND_ADDR", "127.0.0.1:21994")
	c, err = loadConfig(nil)
	if err != nil || c.IMAPAddress != "127.0.0.1:21994" {
		t.Fatal("new bind address did not override legacy bind address", err)
	}
	c, err = loadConfig([]string{"--imap=127.0.0.1:31994", "--max=11", "--imap-username=cli-user"})
	if err != nil || c.IMAPAddress != "127.0.0.1:31994" || c.MaxMessages != 11 || c.Username != "cli-user" {
		t.Fatal("command-line flags did not override environment and YAML", err)
	}
}
