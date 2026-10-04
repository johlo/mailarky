package sandbox

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"testing"
)

func TestCLIInformationalCommandsIgnoreServerConfiguration(t *testing.T) {
	t.Setenv("MAILARKY_CONFIG", filepath.Join(t.TempDir(), "missing.yaml"))
	t.Setenv("MAILARKY_MAX_MESSAGES", "invalid")
	t.Setenv("MAILARKY_PASSWORD", "private-password")
	for _, args := range [][]string{{"--help"}, {"-h"}, {"help"}, {"sendmail", "--help"}, {"--version"}, {"version"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			var output bytes.Buffer
			if err := runCommand(context.Background(), args, &output); err != nil {
				t.Fatal(err)
			}
			text := output.String()
			if text == "" || strings.Contains(text, "private-password") {
				t.Fatalf("unexpected output: %q", text)
			}
			if args[0] == "--help" && (!strings.Contains(text, "MAILARKY_IMAP_CERT") || !strings.Contains(text, "--imap-tls-key")) {
				t.Fatalf("help omitted TLS setup: %s", text)
			}
		})
	}
}

func TestCLIHelpAfterFlagsDoesNotExposeConfiguredCredentials(t *testing.T) {
	config := filepath.Join(t.TempDir(), "mailarky.yaml")
	if err := os.WriteFile(config, []byte("password: yaml-secret\nhttp_auth: admin:http-secret\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MAILARKY_CONFIG", config)
	t.Setenv("MAILARKY_PASSWORD", "env-secret")
	var output bytes.Buffer
	if err := runCommand(context.Background(), []string{"--password=flag-secret", "--help"}, &output); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), "secret") || !strings.Contains(output.String(), "Usage:") {
		t.Fatalf("unexpected help: %s", &output)
	}
}

func TestCLIRejectsInvalidArguments(t *testing.T) {
	for _, args := range [][]string{{"--not-a-flag"}, {"unexpected"}, {"--version", "unexpected"}} {
		var output bytes.Buffer
		if err := runCommand(context.Background(), args, &output); err == nil {
			t.Fatalf("accepted %q", args)
		}
	}
}

func TestCLIMissingCertificateExplainsSetup(t *testing.T) {
	var output bytes.Buffer
	err := runCommand(context.Background(), []string{
		"--imap-tls-cert=" + filepath.Join(t.TempDir(), "missing.crt"),
		"--imap-tls-key=" + filepath.Join(t.TempDir(), "missing.key"),
	}, &output)
	if err == nil || !strings.Contains(err.Error(), "--imap-tls-cert") || !strings.Contains(err.Error(), "--imap-tls-key") {
		t.Fatalf("missing certificate should explain setup: %v", err)
	}
}

func TestBuildVersion(t *testing.T) {
	for _, tc := range []struct {
		name, version, revision string
		info                    *debug.BuildInfo
		want                    string
	}{
		{name: "no metadata", want: "mailarky devel"},
		{name: "installed release", info: &debug.BuildInfo{Main: debug.Module{Version: "v0.1.0"}}, want: "mailarky v0.1.0"},
		{name: "installed revision", info: &debug.BuildInfo{Main: debug.Module{Version: "v0.0.0-20261004150000-1789be0f60fe"}}, want: "mailarky v0.0.0-20261004150000-1789be0f60fe"},
		{name: "local checkout", info: &debug.BuildInfo{Main: debug.Module{Version: "(devel)"}, Settings: []debug.BuildSetting{{Key: "vcs.revision", Value: "1789be0f60fe1baa9013f57b2b6d2aae4ca80e90"}, {Key: "vcs.modified", Value: "true"}}}, want: "mailarky devel 1789be0f60fe dirty"},
		{name: "local module version", info: &debug.BuildInfo{Main: debug.Module{Version: "v0.0.0-20261004151834-1789be0f60fe+dirty"}, Settings: []debug.BuildSetting{{Key: "vcs.revision", Value: "1789be0f60fe1baa9013f57b2b6d2aae4ca80e90"}, {Key: "vcs.modified", Value: "true"}}}, want: "mailarky v0.0.0-20261004151834-1789be0f60fe+dirty"},
		{name: "release override", version: "v0.2.0", revision: "abcdef0123456789", info: &debug.BuildInfo{Main: debug.Module{Version: "v0.1.0"}, Settings: []debug.BuildSetting{{Key: "vcs.revision", Value: "old-commit"}}}, want: "mailarky v0.2.0 abcdef012345"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := formatBuildVersion(tc.info, tc.version, tc.revision); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}
