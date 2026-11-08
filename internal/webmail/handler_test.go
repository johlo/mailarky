package webmail

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestPickerListsAccountsAndCreatesAlpsSessionWithoutExposingCredential(t *testing.T) {
	var apiCalls atomic.Int32
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		apiCalls.Add(1)
		user, password, ok := r.BasicAuth()
		if !ok || user != "operator" || password != "secret" || r.Header.Get("Cookie") != "" {
			t.Error("HTTP auth not forwarded or Alps cookie leaked to control API")
		}
		switch r.Method + " " + r.URL.Path {
		case "GET /prefix/api/v1/accounts":
			io.WriteString(w, `[{"id":"default","name":"Default","username":"user@example.test","created":"2026-11-08T10:00:00Z","password":"never-expose","limits":{}}]`)
		case "POST /prefix/api/v1/accounts/default/webmail-login":
			w.WriteHeader(201)
			io.WriteString(w, `{"username":"user@example.test","password":"temporary-credential","expires_at":"2026-11-08T11:00:00Z"}`)
		default:
			t.Error("unexpected control API request", r.Method, r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer api.Close()
	alps := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" && r.URL.Path == "/" {
			io.WriteString(w, "original Alps application")
			return
		}
		if r.Method != "POST" || r.URL.Path != "/session" || r.RequestURI != "/session" || r.Header.Get("Origin") != "http://picker.test" {
			t.Error("incorrect Alps login request", r.Method, r.URL.Path, r.RequestURI)
		}
		var credentials map[string]string
		if err := json.NewDecoder(r.Body).Decode(&credentials); err != nil || credentials["password"] != "temporary-credential" || credentials["username"] != "user@example.test" {
			t.Error("Alps did not receive the credential", err)
		}
		http.SetCookie(w, &http.Cookie{Name: "alps", Value: "session", HttpOnly: true})
		io.WriteString(w, `{"ok":true}`)
	})
	handler, err := NewHandler(alps, api.URL+"/prefix/")
	if err != nil {
		t.Fatal(err)
	}
	call := func(method, path string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "http://picker.test"+path, nil)
		r.SetBasicAuth("operator", "secret")
		r.Header.Set("Cookie", "alps=previous-session")
		r.Header.Set("Origin", "http://picker.test")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		return w
	}
	page := call("GET", "/")
	if !strings.Contains(page.Body.String(), "Open an account") || apiCalls.Load() != 0 {
		t.Fatal("the start page should be static and available independently of the API")
	}
	if strings.Contains(call("GET", "/mailarky/accounts").Body.String(), "never-expose") {
		t.Fatal("account list leaked an unexpected credential field")
	}
	login := call("POST", "/mailarky/accounts/default/login")
	if strings.Contains(login.Body.String(), "temporary-credential") || !strings.Contains(login.Header().Get("Set-Cookie"), "alps=session") {
		t.Fatal("login must return Alps cookies, not the temporary credential")
	}
	if got := call("GET", "/alps").Body.String(); got != "original Alps application" {
		t.Fatal("original Alps page unavailable", got)
	}
	if apiCalls.Load() != 2 {
		t.Fatal("unexpected API calls", apiCalls.Load())
	}
}

func TestPickerRejectsCrossOriginLoginAndHandlesMissingAccounts(t *testing.T) {
	var apiCalls atomic.Int32
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		apiCalls.Add(1)
		w.WriteHeader(404)
	}))
	defer api.Close()
	handler, err := NewHandler(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("Alps must not be called without a credential")
	}), api.URL)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		origin string
		status int
	}{{"http://unrelated.test", 403}, {"http://picker.test", 404}} {
		request := httptest.NewRequest("POST", "http://picker.test/mailarky/accounts/deleted/login", nil)
		request.Header.Set("Origin", tc.origin)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != tc.status {
			t.Fatal(response.Code, response.Body.String())
		}
	}
	if apiCalls.Load() != 1 {
		t.Fatal("cross-origin login reached the control API")
	}
}

func TestPickerUpstreamErrorsAndOptOut(t *testing.T) {
	for _, code := range []int{401, 500} {
		t.Run(http.StatusText(code), func(t *testing.T) {
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(code)
				io.WriteString(w, "internal-detail-do-not-expose")
			}))
			defer api.Close()
			handler, err := NewHandler(http.NotFoundHandler(), api.URL)
			if err != nil {
				t.Fatal(err)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest("GET", "/mailarky/accounts", nil))
			expected := 502
			if code == 401 {
				expected = 401
				if response.Header().Get("WWW-Authenticate") == "" {
					t.Fatal("HTTP auth challenge missing")
				}
			}
			if response.Code != expected || strings.Contains(response.Body.String(), "internal-detail") || response.Header().Get("Cache-Control") != "no-store" {
				t.Fatal(response.Code, response.Body.String())
			}
		})
	}
	next := http.NotFoundHandler()
	handler, err := NewHandler(next, "")
	if err != nil {
		t.Fatal("empty API URL must preserve upstream Alps")
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest("GET", "/", nil))
	if response.Code != http.StatusNotFound {
		t.Fatal("picker was enabled without an API URL")
	}
	for _, invalid := range []string{"file:///tmp/data", "http://", "http://user:secret@host", "http://host?token=x"} {
		if _, err := NewHandler(next, invalid); err == nil {
			t.Fatal("invalid API URL accepted", invalid)
		}
	}
}
