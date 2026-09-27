package sandbox

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestOpenAPITracksRegisteredHTTPRoutes(t *testing.T) {
	source, err := os.ReadFile("api.go")
	if err != nil {
		t.Fatal(err)
	}
	registered := map[string]bool{}
	for _, match := range regexp.MustCompile(`mux.HandleFunc\("([A-Z]+) ([^"]+)"`).FindAllStringSubmatch(string(source), -1) {
		path := strings.ReplaceAll(match[2], "{$}", "")
		registered[strings.ToLower(match[1])+" "+path] = true
	}
	for _, path := range []string{"/healthz", "/livez", "/readyz"} {
		registered["get "+path] = true
	}
	// Scoped handlers expose the same API, excluding account administration.
	scoped := map[string]bool{}
	for key := range registered {
		method, path, _ := strings.Cut(key, " ")
		if !strings.HasPrefix(path, "/api/v1/mailboxes") {
			scoped[method+" /mailboxes/{mailbox}"+path] = true
		}
	}
	for key := range scoped {
		registered[key] = true
	}
	var spec struct {
		Paths map[string]map[string]any `yaml:"paths"`
	}
	data, err := os.ReadFile("../../openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if err := yaml.Unmarshal(data, &spec); err != nil {
		t.Fatal(err)
	}
	for path, methods := range spec.Paths {
		for method := range methods {
			key := method + " " + path
			if !registered[key] {
				t.Errorf("spec route missing from server: %s", key)
			}
			delete(registered, key)
		}
	}
	for key := range registered {
		t.Errorf("server route missing from spec: %s", key)
	}
}
