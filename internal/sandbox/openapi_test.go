package sandbox

import (
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestOpenAPITracksRegisteredRoutes(t *testing.T) {
	source, err := os.ReadFile("api.go")
	if err != nil {
		t.Fatal(err)
	}
	registered := map[string]bool{}
	for _, match := range regexp.MustCompile(`mux.HandleFunc\("([A-Z]+) ([^"]+)"`).FindAllStringSubmatch(string(source), -1) {
		path := strings.ReplaceAll(match[2], "{$}", "")
		registered[strings.ToLower(match[1])+" "+path] = true
	}
	for _, prefix := range []string{"/api/v1/faults", "/api/v1/accounts/{account}/faults"} {
		for _, method := range []string{"get", "post"} {
			registered[method+" "+prefix] = true
		}
		for _, method := range []string{"get", "put", "patch", "delete"} {
			registered[method+" "+prefix+"/{name}"] = true
		}
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
				t.Errorf("spec endpoint missing from server: %s", key)
			}
			delete(registered, key)
		}
	}
	for key := range registered {
		t.Errorf("server endpoint missing from spec: %s", key)
	}
}
func TestOpenAPIMessageSchemasMatchActualJSON(t *testing.T) {
	a := testStore(t, defaultConfig())
	m := appendTest(t, a, "schema@test")
	var spec struct {
		Components struct {
			Schemas map[string]struct {
				Properties map[string]any `yaml:"properties"`
			} `yaml:"schemas"`
		} `yaml:"components"`
	}
	data, err := os.ReadFile("../../openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if err := yaml.Unmarshal(data, &spec); err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]any{"Message": m.detail(), "MessageSummary": m.summary(), "Account": a.service.description(a.identity)} {
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		var actual map[string]any
		json.Unmarshal(raw, &actual)
		properties := spec.Components.Schemas[name].Properties
		for key := range actual {
			if _, ok := properties[key]; !ok {
				t.Errorf("%s field undocumented: %s", name, key)
			}
		}
		for key := range properties {
			if _, ok := actual[key]; !ok {
				t.Errorf("%s field missing from response: %s", name, key)
			}
		}
	}
}
