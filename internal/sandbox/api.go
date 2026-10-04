package sandbox

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/johlo/go-imap/v2"
)

type httpAPI struct{ service *Service }
type accountAPI struct{ account *Account }

func newAPI(service *Service) *httpAPI { return &httpAPI{service: service} }
func jsonResponse(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func apiError(w http.ResponseWriter, status int, err error) {
	jsonResponse(w, status, map[string]string{"error": err.Error()})
}
func decodeJSON(w http.ResponseWriter, r *http.Request, size int64, value any) error {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, size))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return errors.New("request must contain exactly one JSON value")
	}
	return nil
}
func (a *httpAPI) withAccount(fn func(*accountAPI, http.ResponseWriter, *http.Request)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		account := a.service.lookup(r.PathValue("account"))
		if account == nil {
			apiError(w, 404, errors.New("account not found"))
			return
		}
		fn(&accountAPI{account: account}, w, r)
	}
}
func (a *httpAPI) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { jsonResponse(w, 200, map[string]string{"status": "ok"}) })
	mux.HandleFunc("GET /api/v1/info", a.info)
	mux.HandleFunc("GET /api/v1/accounts", a.listAccounts)
	mux.HandleFunc("POST /api/v1/accounts", a.createAccount)
	mux.HandleFunc("GET /api/v1/accounts/{account}", a.withAccount((*accountAPI).description))
	mux.HandleFunc("DELETE /api/v1/accounts/{account}", a.deleteAccount)
	mux.HandleFunc("GET /api/v1/accounts/{account}/messages", a.withAccount((*accountAPI).messages))
	mux.HandleFunc("POST /api/v1/accounts/{account}/messages", a.withAccount((*accountAPI).createMessage))
	mux.HandleFunc("DELETE /api/v1/accounts/{account}/messages", a.withAccount((*accountAPI).deleteMessages))
	mux.HandleFunc("GET /api/v1/accounts/{account}/messages/{id}", a.withAccount((*accountAPI).message))
	mux.HandleFunc("PATCH /api/v1/accounts/{account}/messages/{id}", a.withAccount((*accountAPI).setFlags))
	mux.HandleFunc("DELETE /api/v1/accounts/{account}/messages/{id}", a.withAccount((*accountAPI).deleteMessage))
	mux.HandleFunc("GET /api/v1/accounts/{account}/messages/{id}/raw", a.withAccount((*accountAPI).raw))
	mux.HandleFunc("GET /api/v1/accounts/{account}/messages/{id}/headers", a.withAccount((*accountAPI).headers))
	mux.HandleFunc("GET /api/v1/accounts/{account}/messages/{id}/parts/{part}", a.withAccount((*accountAPI).part))
	mux.HandleFunc("GET /api/v1/accounts/{account}/messages/{id}/view", a.withAccount((*accountAPI).view))
	mux.HandleFunc("GET /api/v1/accounts/{account}/folders", a.withAccount((*accountAPI).folders))
	mux.HandleFunc("POST /api/v1/accounts/{account}/folders", a.withAccount((*accountAPI).createFolder))
	mux.HandleFunc("GET /api/v1/accounts/{account}/events", a.withAccount((*accountAPI).events))
	mux.HandleFunc("GET /metrics", a.metrics)
	for _, prefix := range []string{"/api/v1/faults", "/api/v1/accounts/{account}/faults"} {
		mux.HandleFunc("GET "+prefix, a.faults)
		mux.HandleFunc("POST "+prefix, a.faults)
		mux.HandleFunc("GET "+prefix+"/{name}", a.faults)
		mux.HandleFunc("PUT "+prefix+"/{name}", a.faults)
		mux.HandleFunc("PATCH "+prefix+"/{name}", a.faults)
		mux.HandleFunc("DELETE "+prefix+"/{name}", a.faults)
	}
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, 200, map[string]any{"service": "mailarky", "api": a.service.config.Webroot + "/api/v1/accounts", "protocols": []string{"smtp", "imap", "http"}})
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { apiError(w, 404, errors.New("endpoint not found")) })
	var handler http.Handler = mux
	if root := a.service.config.Webroot; root != "" {
		handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != root && !strings.HasPrefix(r.URL.Path, root+"/") {
				apiError(w, 404, errors.New("endpoint not found"))
				return
			}
			http.StripPrefix(root, mux).ServeHTTP(w, r)
		})
	}
	return a.middleware(handler)
}
func (a *httpAPI) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if r.URL.Path != a.service.config.Webroot+"/healthz" && len(a.service.httpUsers) > 0 {
			username, password, ok := r.BasicAuth()
			if !ok || !a.service.httpUsers.valid(username, password) {
				w.Header().Set("WWW-Authenticate", `Basic realm="mailarky"`)
				apiError(w, 401, errors.New("authentication required"))
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}
func (a *httpAPI) listAccounts(w http.ResponseWriter, r *http.Request) {
	h := a.service
	h.mu.RLock()
	out := []mailboxDescription{}
	for _, account := range h.entries {
		out = append(out, h.description(account.identity))
	}
	h.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	jsonResponse(w, 200, out)
}
func (a *httpAPI) createAccount(w http.ResponseWriter, r *http.Request) {
	var req mailboxCreation
	if err := decodeJSON(w, r, 64<<10, &req); err != nil {
		apiError(w, 400, err)
		return
	}
	result, err := a.service.create(req)
	if err != nil {
		apiError(w, 400, err)
		return
	}
	jsonResponse(w, 201, result)
}
func (a *accountAPI) description(w http.ResponseWriter, r *http.Request) {
	jsonResponse(w, 200, a.account.service.description(a.account.identity))
}
func (a *httpAPI) deleteAccount(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("account")
	if a.service.lookup(id) == nil {
		apiError(w, 404, errors.New("account not found"))
		return
	}
	if err := a.service.remove(id); err != nil {
		apiError(w, 400, err)
		return
	}
	w.WriteHeader(204)
}

func (a *accountAPI) createMessage(w http.ResponseWriter, r *http.Request) {
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil {
		apiError(w, 415, errors.New("Content-Type must be application/json or message/rfc822"))
		return
	}
	var raw []byte
	options := appendOptions{Folder: "INBOX", Notify: true}
	switch media {
	case "application/json":
		var req messageRequest
		if err := decodeJSON(w, r, a.account.options.MaxSize, &req); err != nil {
			apiError(w, 400, err)
			return
		}
		if req.Folder != "" {
			options.Folder = req.Folder
		}
		options.Flags = req.Flags
		source, date, err := req.rawMessage()
		if err != nil {
			apiError(w, 400, err)
			return
		}
		raw = []byte(source)
		options.Date = date
	case "message/rfc822":
		raw, err = io.ReadAll(http.MaxBytesReader(w, r.Body, a.account.options.MaxSize))
		if err != nil {
			apiError(w, 400, err)
			return
		}
		if folder := r.URL.Query().Get("folder"); folder != "" {
			options.Folder = folder
		}
	default:
		apiError(w, 415, errors.New("Content-Type must be application/json or message/rfc822"))
		return
	}
	m, err := a.account.append(raw, options)
	if err != nil {
		apiError(w, 400, err)
		return
	}
	jsonResponse(w, 201, m.detail())
}

type messagesSummary struct {
	Messages    []messageSummary `json:"messages"`
	Start       int              `json:"start"`
	Total       int              `json:"total"`
	Unread      int              `json:"unread"`
	Count       int              `json:"matched"`
	UnreadCount int              `json:"matched_unread"`
}

func (a *accountAPI) messages(w http.ResponseWriter, r *http.Request) {
	predicate, err := compileSearch(r.URL.Query().Get("query"), r.URL.Query().Get("tz"))
	if err != nil {
		apiError(w, 400, err)
		return
	}
	start, limit := 0, 50
	for key, p := range map[string]*int{"start": &start, "limit": &limit} {
		if raw := r.URL.Query().Get(key); raw != "" {
			n, err := strconv.Atoi(raw)
			if err != nil || n < 0 || (key == "limit" && (n == 0 || n > 10000)) {
				apiError(w, 400, errors.New("invalid pagination"))
				return
			}
			*p = n
		}
	}
	list := a.account.snapshot()
	result := messagesSummary{Messages: []messageSummary{}, Start: start, Total: len(list)}
	for i := len(list) - 1; i >= 0; i-- {
		m := list[i]
		unread := !hasFlag(m.Flags, string(imap.FlagSeen))
		if unread {
			result.Unread++
		}
		if !predicate(m) {
			continue
		}
		if unread {
			result.UnreadCount++
		}
		if result.Count >= start && len(result.Messages) < limit {
			result.Messages = append(result.Messages, m.summary())
		}
		result.Count++
	}
	jsonResponse(w, 200, result)
}
func (a *accountAPI) requireMessage(w http.ResponseWriter, r *http.Request) *storedMessage {
	m := a.account.get(r.PathValue("id"))
	if m == nil {
		apiError(w, 404, errors.New("message not found"))
	}
	return m
}
func (a *accountAPI) message(w http.ResponseWriter, r *http.Request) {
	if m := a.requireMessage(w, r); m != nil {
		jsonResponse(w, 200, m.detail())
	}
}
func (a *accountAPI) raw(w http.ResponseWriter, r *http.Request) {
	if m := a.requireMessage(w, r); m != nil {
		w.Header().Set("Content-Type", "message/rfc822")
		_, _ = w.Write(m.Raw)
	}
}
func (a *accountAPI) headers(w http.ResponseWriter, r *http.Request) {
	if m := a.requireMessage(w, r); m != nil {
		jsonResponse(w, 200, m.Headers)
	}
}
func (a *accountAPI) part(w http.ResponseWriter, r *http.Request) {
	m := a.requireMessage(w, r)
	if m == nil {
		return
	}
	for _, parts := range [][]attachment{m.Detail.Attachments, m.Detail.Inline} {
		for _, part := range parts {
			if part.PartID == r.PathValue("part") {
				data, err := readMIMEPart(m.Raw, part.PartID)
				if err != nil {
					apiError(w, 500, err)
					return
				}
				w.Header().Set("Content-Type", part.ContentType)
				w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": part.FileName}))
				_, _ = w.Write(data)
				return
			}
		}
	}
	apiError(w, 404, errors.New("MIME part not found"))
}
func (a *accountAPI) view(w http.ResponseWriter, r *http.Request) {
	m := a.requireMessage(w, r)
	if m == nil {
		return
	}
	if r.URL.Query().Get("format") == "text" {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = io.WriteString(w, m.Detail.Text)
	} else {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'; style-src 'unsafe-inline'; img-src data:")
		_, _ = io.WriteString(w, m.Detail.HTML)
	}
}
func (a *accountAPI) deleteMessages(w http.ResponseWriter, r *http.Request) {
	var body struct {
		IDs []string `json:"ids"`
	}
	if r.Body != nil && r.ContentLength != 0 {
		if err := decodeJSON(w, r, 1<<20, &body); err != nil {
			apiError(w, 400, err)
			return
		}
	}
	query := r.URL.Query().Get("query")
	if len(body.IDs) > 0 && query != "" {
		apiError(w, 400, errors.New("use ids or query"))
		return
	}
	predicate, err := compileSearch(query, r.URL.Query().Get("tz"))
	if err != nil {
		apiError(w, 400, err)
		return
	}
	ids := map[string]bool{}
	for _, id := range body.IDs {
		ids[id] = true
	}
	// Search and deletion share a single mutation, so concurrent pruning cannot
	// invalidate a snapshot and turn an otherwise successful delete into a 500.
	err = a.account.mutate(func(s *storeState) error {
		for id, m := range s.Messages {
			if (len(ids) > 0 && ids[id]) || (len(ids) == 0 && predicate(m)) {
				delete(s.Messages, id)
			}
		}
		return nil
	})
	if err != nil {
		apiError(w, 500, err)
		return
	}
	w.WriteHeader(204)
}
func (a *accountAPI) deleteMessage(w http.ResponseWriter, r *http.Request) {
	if err := a.account.mutate(func(s *storeState) error { delete(s.Messages, r.PathValue("id")); return nil }); err != nil {
		apiError(w, 500, err)
		return
	}
	w.WriteHeader(204)
}
func (a *accountAPI) setFlags(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Flags *[]string `json:"flags"`
	}
	if err := decodeJSON(w, r, 64<<10, &body); err != nil || body.Flags == nil {
		apiError(w, 400, errors.New("flags is required"))
		return
	}
	for _, f := range *body.Flags {
		if f == "" || strings.ContainsAny(f, " (){\r\n\x00") {
			apiError(w, 400, errors.New("invalid flag"))
			return
		}
	}
	err := a.account.mutate(func(s *storeState) error {
		m := s.Messages[r.PathValue("id")]
		if m == nil {
			return errors.New("message not found")
		}
		copy := cloneRecord(m)
		copy.Flags = append([]string{}, (*body.Flags)...)
		s.Messages[m.ID] = copy
		return nil
	})
	if err != nil {
		apiError(w, 404, err)
		return
	}
	a.message(w, r)
}
func (a *accountAPI) folders(w http.ResponseWriter, r *http.Request) {
	folders, err := a.account.user.ListMailboxes(false)
	if err != nil {
		apiError(w, 500, err)
		return
	}
	result := []any{}
	for _, f := range folders {
		status, err := f.Status()
		if err != nil {
			apiError(w, 500, err)
			return
		}
		result = append(result, map[string]any{"name": f.Name(), "messages": status.NumMessages, "uid_next": status.UIDNext, "uid_validity": status.UIDValidity})
	}
	jsonResponse(w, 200, result)
}
func (a *accountAPI) createFolder(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string `json:"name"`
	}
	if err := decodeJSON(w, r, 1024, &body); err != nil {
		apiError(w, 400, err)
		return
	}
	if err := a.account.user.CreateMailbox(body.Name); err != nil {
		apiError(w, 400, err)
		return
	}
	jsonResponse(w, 201, body)
}

func (a *httpAPI) faults(w http.ResponseWriter, r *http.Request) {
	registry, scope := a.service.serverFaults, "server"
	if id := r.PathValue("account"); id != "" {
		account := a.service.lookup(id)
		if account == nil {
			apiError(w, 404, errors.New("account not found"))
			return
		}
		registry, scope = account.faults, "account"
	}
	name := r.PathValue("name")
	switch r.Method {
	case "GET":
		if name == "" {
			jsonResponse(w, 200, registry.list())
			return
		}
		if t, ok := registry.get(name); ok {
			jsonResponse(w, 200, t)
		} else {
			apiError(w, 404, errors.New("fault not found"))
		}
	case "POST", "PUT":
		rule := faultRule{Enabled: true}
		if err := decodeJSON(w, r, 64<<10, &rule); err != nil {
			apiError(w, 400, err)
			return
		}
		if name != "" {
			if rule.Name != "" && rule.Name != name {
				apiError(w, 400, errors.New("fault name must match URL"))
				return
			}
			rule.Name = name
		}
		if err := registry.put(rule, r.Method == "POST", scope); err != nil {
			apiError(w, 400, err)
			return
		}
		result, _ := registry.get(rule.Name)
		status := 200
		if r.Method == "POST" {
			status = 201
		}
		jsonResponse(w, status, result)
	case "PATCH":
		var body struct {
			Enabled *bool `json:"enabled"`
		}
		if err := decodeJSON(w, r, 1024, &body); err != nil || body.Enabled == nil {
			apiError(w, 400, errors.New("enabled is required"))
			return
		}
		if t, ok := registry.toggle(name, *body.Enabled); ok {
			jsonResponse(w, 200, t)
		} else {
			apiError(w, 404, errors.New("fault not found"))
		}
	case "DELETE":
		if !registry.remove(name) {
			apiError(w, 404, errors.New("fault not found"))
			return
		}
		w.WriteHeader(204)
	}
}
