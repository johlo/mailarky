// Package webmail adds the Mailarky account picker in front of Alps. It uses
// only the standard library so the Docker build can compile it into Alps.
package webmail

import (
	"bytes"
	"embed"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

//go:embed assets/*
var assets embed.FS

type picker struct {
	next   http.Handler
	apiURL string
	client *http.Client
}

// NewHandler leaves Alps unchanged when apiURL is empty. Otherwise the picker
// is the entry page, with the original Alps application available at /alps.
func NewHandler(next http.Handler, apiURL string) (http.Handler, error) {
	if apiURL == "" {
		return next, nil
	}
	u, err := url.Parse(apiURL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("MAILARKY_API_URL must be an HTTP(S) URL without credentials, query, or fragment")
	}
	p := &picker{next: next, apiURL: strings.TrimRight(apiURL, "/"), client: &http.Client{
		Timeout:       10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", p.page)
	mux.HandleFunc("GET /mailarky/picker.css", p.asset)
	mux.HandleFunc("GET /mailarky/picker.js", p.asset)
	mux.HandleFunc("GET /mailarky/alps.js", p.asset)
	mux.HandleFunc("GET /mailarky/logo.svg", p.asset)
	mux.HandleFunc("GET /mailarky/accounts", p.accounts)
	mux.Handle("POST /mailarky/accounts/{account}/login", http.NewCrossOriginProtection().Handler(http.HandlerFunc(p.login)))
	mux.HandleFunc("GET /alps", func(w http.ResponseWriter, r *http.Request) {
		r = r.Clone(r.Context())
		r.URL.Path, r.RequestURI = "/", "/"
		next.ServeHTTP(w, r)
	})
	mux.Handle("/", next)
	return mux, nil
}

func pickerHeaders(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
}

func (p *picker) page(w http.ResponseWriter, r *http.Request) {
	pickerHeaders(w)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	data, _ := assets.ReadFile("assets/index.html")
	w.Write(data)
}

func (p *picker) asset(w http.ResponseWriter, r *http.Request) {
	pickerHeaders(w)
	name := strings.TrimPrefix(r.URL.Path, "/mailarky/")
	switch name {
	case "picker.css":
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
	case "logo.svg":
		w.Header().Set("Content-Type", "image/svg+xml")
	default:
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	}
	data, _ := assets.ReadFile("assets/" + name)
	w.Write(data)
}

// Forward only the control API's HTTP authentication, never Alps cookies.
// The browser must satisfy the same API permissions as any other operator.
func (p *picker) request(r *http.Request, method, path string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(r.Context(), method, p.apiURL+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", r.Header.Get("Authorization"))
	return p.client.Do(req)
}

func pickerError(w http.ResponseWriter, status int, message string) {
	pickerHeaders(w)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": message})
}

func upstreamError(w http.ResponseWriter, response *http.Response) {
	switch response.StatusCode {
	case http.StatusUnauthorized:
		w.Header().Set("WWW-Authenticate", `Basic realm="Mailarky accounts"`)
		pickerError(w, http.StatusUnauthorized, "Sign in with Mailarky's HTTP credentials to access accounts.")
	case http.StatusNotFound:
		pickerError(w, http.StatusNotFound, "This account no longer exists. Refresh the account list.")
	default:
		pickerError(w, http.StatusBadGateway, "Mailarky is unavailable. Try refreshing the account list.")
	}
}

func (p *picker) accounts(w http.ResponseWriter, r *http.Request) {
	response, err := p.request(r, http.MethodGet, "/api/v1/accounts")
	if err != nil {
		pickerError(w, http.StatusBadGateway, "Cannot reach Mailarky. Check that the service is running.")
		return
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		upstreamError(w, response)
		return
	}
	// Expose only the fields the picker needs, not credentials or future API fields.
	var accounts []struct {
		ID       string    `json:"id"`
		Name     string    `json:"name"`
		Username string    `json:"username"`
		Created  time.Time `json:"created"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 4<<20)).Decode(&accounts); err != nil || accounts == nil {
		pickerError(w, http.StatusBadGateway, "Could not read the account list from Mailarky.")
		return
	}
	pickerHeaders(w)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(accounts)
}

func (p *picker) login(w http.ResponseWriter, r *http.Request) {
	response, err := p.request(r, http.MethodPost, "/api/v1/accounts/"+url.PathEscape(r.PathValue("account"))+"/webmail-login")
	if err != nil {
		pickerError(w, http.StatusBadGateway, "Cannot reach Mailarky. Check that the service is running.")
		return
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		upstreamError(w, response)
		return
	}
	var credentials struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 4096)).Decode(&credentials); err != nil || credentials.Username == "" || credentials.Password == "" {
		pickerError(w, http.StatusBadGateway, "Could not prepare the account login. Try again.")
		return
	}
	// Establish a normal Alps session. Its cookies and login errors are returned
	// to the browser, but the temporary password stays between the two servers.
	body, _ := json.Marshal(credentials)
	r = r.Clone(r.Context())
	r.URL.Path, r.URL.RawPath, r.URL.RawQuery, r.RequestURI = "/session", "", "", "/session"
	r.Body = io.NopCloser(bytes.NewReader(body))
	r.ContentLength = int64(len(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Del("Content-Length")
	pickerHeaders(w)
	p.next.ServeHTTP(w, r)
}
