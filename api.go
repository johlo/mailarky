package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/backend/backendutil"
)

type httpAPI struct {
	store          *mailboxBackend
	auth, sendAuth credentials
}

func newAPI(store *mailboxBackend) (*httpAPI, error) {
	auth, err := readCredentials(store.config.HTTPAuthFile, store.config.HTTPAuth)
	if err != nil {
		return nil, err
	}
	send, err := readCredentials(store.config.SendAuthFile, store.config.SendAuth)
	if err != nil {
		return nil, err
	}
	return &httpAPI{store: store, auth: auth, sendAuth: send}, nil
}
func jsonResponse(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(value)
}
func apiError(w http.ResponseWriter, status int, err error) { http.Error(w, err.Error(), status) }
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

func (a *httpAPI) handler() http.Handler {
	mux := http.NewServeMux()
	for _, path := range []string{"/healthz", "/livez", "/readyz"} {
		mux.HandleFunc("GET "+path, func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok\n")) })
	}
	mux.HandleFunc("POST /messages", a.fixture)
	mux.HandleFunc("GET /api/v1/messages", a.messages)
	mux.HandleFunc("GET /api/v1/search", a.messages)
	mux.HandleFunc("DELETE /api/v1/search", a.deleteSearch)
	mux.HandleFunc("DELETE /api/v1/messages", a.deleteMessages)
	mux.HandleFunc("PUT /api/v1/messages", a.readMessages)
	mux.HandleFunc("GET /api/v1/message/{id}", a.message)
	mux.HandleFunc("GET /api/v1/message/{id}/raw", a.raw)
	mux.HandleFunc("GET /api/v1/message/{id}/headers", a.headers)
	mux.HandleFunc("GET /api/v1/message/{id}/part/{part}", a.part)
	mux.HandleFunc("GET /api/v1/message/{id}/part/{part}/thumb", a.thumbnail)
	mux.HandleFunc("POST /api/v1/message/{id}/release", a.release)
	mux.HandleFunc("GET /api/v1/message/{id}/link-check", a.linkCheck)
	mux.HandleFunc("GET /api/v1/message/{id}/html-check", a.htmlCheck)
	mux.HandleFunc("GET /api/v1/message/{id}/sa-check", a.spamCheck)
	mux.HandleFunc("POST /api/v1/send", a.send)
	mux.HandleFunc("POST /api/v1/messages/raw", a.importRaw)
	mux.HandleFunc("GET /api/v1/tags", a.tags)
	mux.HandleFunc("PUT /api/v1/tags", a.setTags)
	mux.HandleFunc("PUT /api/v1/tags/{tag}", a.renameTag)
	mux.HandleFunc("DELETE /api/v1/tags/{tag}", a.removeTag)
	mux.HandleFunc("GET /api/v1/info", a.info)
	mux.HandleFunc("GET /api/v1/webui", a.capabilities)
	mux.HandleFunc("GET /api/v1/toxics", a.listToxics)
	mux.HandleFunc("POST /api/v1/toxics", a.createToxic)
	mux.HandleFunc("GET /api/v1/toxics/{name}", a.getToxic)
	mux.HandleFunc("PUT /api/v1/toxics/{name}", a.updateToxic)
	mux.HandleFunc("PATCH /api/v1/toxics/{name}", a.toggleToxic)
	mux.HandleFunc("DELETE /api/v1/toxics/{name}", a.deleteToxic)
	// Legacy global chaos cannot isolate concurrent tests. Make this explicit,
	// rather than silently accepting a configuration that affects everyone.
	mux.HandleFunc("GET /api/v1/chaos", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, 200, map[string]any{"Enabled": false, "MessageScoped": true, "Toxics": "/api/v1/toxics"})
	})
	mux.HandleFunc("PUT /api/v1/chaos", func(w http.ResponseWriter, r *http.Request) {
		apiError(w, 400, errors.New("global SMTP chaos is disabled; create a message-scoped toxic at /api/v1/toxics"))
	})
	mux.HandleFunc("GET /api/v1/folders", a.folders)
	mux.HandleFunc("POST /api/v1/folders", a.createFolder)
	mux.HandleFunc("GET /api/events", a.events)
	mux.HandleFunc("GET /metrics", a.metrics)
	mux.HandleFunc("GET /view/{file}", a.view)
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, 200, map[string]any{"service": "mail-emulator", "protocols": []string{"smtp", "imap", "http"}, "api": "/api/v1/messages"})
	})
	var handler http.Handler = mux
	root := strings.Trim(a.store.config.Webroot, "/")
	if root != "" {
		handler = http.StripPrefix("/"+root, mux)
	}
	return a.middleware(handler)
}

func (a *httpAPI) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		c := a.store.config
		if c.AllowedHosts != "" {
			host := r.Host
			if h, _, err := net.SplitHostPort(host); err == nil {
				host = h
			}
			allowed := false
			for _, candidate := range strings.Split(c.AllowedHosts, ",") {
				if strings.EqualFold(strings.TrimSpace(candidate), host) {
					allowed = true
				}
			}
			if !allowed {
				apiError(w, 403, errors.New("host is not allowed"))
				return
			}
		}
		if origin := r.Header.Get("Origin"); origin != "" && c.CORS != "" {
			for _, candidate := range strings.Split(c.CORS, ",") {
				if candidate == "*" || strings.TrimSpace(candidate) == origin {
					w.Header().Set("Access-Control-Allow-Origin", origin)
					w.Header().Set("Vary", "Origin")
					w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
					w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
					break
				}
			}
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(204)
			return
		}
		path := strings.TrimPrefix(r.URL.Path, "/"+strings.Trim(c.Webroot, "/"))
		if !strings.HasPrefix(path, "/") {
			path = "/" + path
		}
		if path != "/healthz" && path != "/livez" && path != "/readyz" {
			users := a.auth
			acceptAny := false
			if path == "/api/v1/send" && (len(a.sendAuth) > 0 || c.SendAcceptAny) {
				users = a.sendAuth
				acceptAny = c.SendAcceptAny
			}
			if len(users) > 0 || acceptAny {
				user, password, ok := r.BasicAuth()
				if !ok || (!acceptAny && !users.valid(user, password)) {
					w.Header().Set("WWW-Authenticate", `Basic realm="mail-emulator"`)
					apiError(w, 401, errors.New("authentication required"))
					return
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (a *httpAPI) fixture(w http.ResponseWriter, r *http.Request) {
	var req messageRequest
	if err := decodeJSON(w, r, 1<<20, &req); err != nil {
		apiError(w, 400, errors.New("invalid message JSON"))
		return
	}
	if req.Folder == "" {
		req.Folder = "INBOX"
	}
	if _, err := a.store.user.GetMailbox(req.Folder); err != nil {
		apiError(w, 400, errors.New("folder does not exist"))
		return
	}
	raw, date, err := req.rawMessage()
	if err != nil {
		apiError(w, 400, err)
		return
	}
	m, err := a.store.append([]byte(raw), appendOptions{Folder: req.Folder, Date: date, Flags: req.Flags, Notify: true})
	if err != nil {
		apiError(w, 400, err)
		return
	}
	jsonResponse(w, 201, map[string]string{"folder": m.Folder, "message_id": m.Detail.MessageID})
}

type messagesSummary struct {
	Messages    []messageSummary `json:"messages"`
	Start       int              `json:"start"`
	Total       int              `json:"total"`
	Unread      int              `json:"unread"`
	Count       int              `json:"messages_count"`
	UnreadCount int              `json:"messages_unread"`
	Tags        []string         `json:"tags"`
}

func (a *httpAPI) messages(w http.ResponseWriter, r *http.Request) {
	predicate, err := compileSearch(r.URL.Query().Get("query"), r.URL.Query().Get("tz"))
	if err != nil {
		apiError(w, 400, err)
		return
	}
	start, limit := 0, 50
	for key, pointer := range map[string]*int{"start": &start, "limit": &limit} {
		if raw := r.URL.Query().Get(key); raw != "" {
			value, err := strconv.Atoi(raw)
			if err != nil || value < 0 || (key == "limit" && (value == 0 || value > 10000)) {
				apiError(w, 400, errors.New("invalid pagination"))
				return
			}
			*pointer = value
		}
	}
	list := a.store.snapshot()
	result := messagesSummary{Messages: []messageSummary{}, Start: start, Total: len(list), Tags: []string{}}
	tags := map[string]bool{}
	for i := len(list) - 1; i >= 0; i-- {
		m := list[i]
		unread := !hasFlag(m.Flags, imap.SeenFlag)
		if unread {
			result.Unread++
		}
		for _, tag := range m.Tags {
			tags[tag] = true
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
	for tag := range tags {
		result.Tags = append(result.Tags, tag)
	}
	sort.Strings(result.Tags)
	jsonResponse(w, 200, result)
}
func (a *httpAPI) requireMessage(w http.ResponseWriter, r *http.Request) *storedMessage {
	m := a.store.get(r.PathValue("id"))
	if m == nil {
		apiError(w, 404, errors.New("message not found"))
	}
	return m
}
func (a *httpAPI) message(w http.ResponseWriter, r *http.Request) {
	m := a.requireMessage(w, r)
	if m == nil {
		return
	}
	if err := a.setRead([]string{m.ID}, true); err != nil {
		apiError(w, 500, err)
		return
	}
	m.Flags = backendutil.UpdateFlags(m.Flags, imap.AddFlags, []string{imap.SeenFlag})
	jsonResponse(w, 200, m.detail())
}
func (a *httpAPI) raw(w http.ResponseWriter, r *http.Request) {
	m := a.requireMessage(w, r)
	if m == nil {
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Write(m.Raw)
}
func (a *httpAPI) headers(w http.ResponseWriter, r *http.Request) {
	m := a.requireMessage(w, r)
	if m != nil {
		jsonResponse(w, 200, m.Headers)
	}
}
func (a *httpAPI) part(w http.ResponseWriter, r *http.Request) {
	m := a.requireMessage(w, r)
	if m == nil {
		return
	}
	for _, part := range append(m.Detail.Attachments, m.Detail.Inline...) {
		if part.PartID == r.PathValue("part") {
			w.Header().Set("Content-Type", part.ContentType)
			w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": part.FileName}))
			w.Write(part.Data)
			return
		}
	}
	apiError(w, 404, errors.New("MIME part not found"))
}

func (a *httpAPI) remove(ids []string) error {
	return a.store.mutate(func(s *storeState) error {
		for _, id := range ids {
			if s.Messages[id] == nil {
				return fmt.Errorf("message %s not found", id)
			}
		}
		for _, id := range ids {
			delete(s.Messages, id)
		}
		return nil
	})
}
func (a *httpAPI) deleteMessages(w http.ResponseWriter, r *http.Request) {
	var body struct{ IDs []string }
	if r.ContentLength != 0 {
		if err := decodeJSON(w, r, 1<<20, &body); err != nil {
			apiError(w, 400, err)
			return
		}
	}
	if len(body.IDs) == 0 {
		for _, m := range a.store.snapshot() {
			body.IDs = append(body.IDs, m.ID)
		}
	}
	if err := a.remove(body.IDs); err != nil {
		apiError(w, 400, err)
		return
	}
	w.Write([]byte("ok\n"))
}
func (a *httpAPI) deleteSearch(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query().Get("query")
	if strings.TrimSpace(query) == "" {
		apiError(w, 400, errors.New("query is required"))
		return
	}
	predicate, err := compileSearch(query, r.URL.Query().Get("tz"))
	if err != nil {
		apiError(w, 400, err)
		return
	}
	var ids []string
	for _, m := range a.store.snapshot() {
		if predicate(m) {
			ids = append(ids, m.ID)
		}
	}
	if err := a.remove(ids); err != nil {
		apiError(w, 500, err)
		return
	}
	w.Write([]byte("ok\n"))
}
func (a *httpAPI) setRead(ids []string, read bool) error {
	return a.store.mutate(func(s *storeState) error {
		for _, id := range ids {
			m := s.Messages[id]
			if m == nil {
				return errors.New("message not found")
			}
			copy := cloneRecord(m)
			operation := imap.FlagsOp(imap.RemoveFlags)
			if read {
				operation = imap.AddFlags
			}
			copy.Flags = backendutil.UpdateFlags(copy.Flags, operation, []string{imap.SeenFlag})
			s.Messages[id] = copy
		}
		return nil
	})
}
func (a *httpAPI) readMessages(w http.ResponseWriter, r *http.Request) {
	var body struct {
		IDs    []string
		Read   *bool
		Search string
	}
	if err := decodeJSON(w, r, 1<<20, &body); err != nil || body.Read == nil {
		apiError(w, 400, errors.New("Read is required"))
		return
	}
	if len(body.IDs) > 0 && body.Search != "" {
		apiError(w, 400, errors.New("use IDs or Search"))
		return
	}
	if len(body.IDs) == 0 {
		predicate, err := compileSearch(body.Search, r.URL.Query().Get("tz"))
		if err != nil {
			apiError(w, 400, err)
			return
		}
		for _, m := range a.store.snapshot() {
			if predicate(m) {
				body.IDs = append(body.IDs, m.ID)
			}
		}
	}
	if err := a.setRead(body.IDs, *body.Read); err != nil {
		apiError(w, 400, err)
		return
	}
	w.Write([]byte("ok\n"))
}

func (a *httpAPI) tags(w http.ResponseWriter, r *http.Request) {
	unique := map[string]bool{}
	for _, m := range a.store.snapshot() {
		for _, tag := range m.Tags {
			unique[tag] = true
		}
	}
	tags := []string{}
	for tag := range unique {
		tags = append(tags, tag)
	}
	sort.Strings(tags)
	jsonResponse(w, 200, tags)
}
func (a *httpAPI) setTags(w http.ResponseWriter, r *http.Request) {
	var body struct {
		IDs  []string
		Tags []string
	}
	if err := decodeJSON(w, r, 1<<20, &body); err != nil || len(body.IDs) == 0 {
		apiError(w, 400, errors.New("IDs and Tags are required"))
		return
	}
	tags, err := normalizeTags(body.Tags)
	if err != nil {
		apiError(w, 400, err)
		return
	}
	err = a.store.mutate(func(s *storeState) error {
		for _, id := range body.IDs {
			m := s.Messages[id]
			if m == nil {
				return errors.New("message not found")
			}
			copy := cloneRecord(m)
			copy.Tags = tags
			s.Messages[id] = copy
		}
		return nil
	})
	if err != nil {
		apiError(w, 400, err)
		return
	}
	w.Write([]byte("ok\n"))
}
func (a *httpAPI) changeTag(from, to string) error {
	return a.store.mutate(func(s *storeState) error {
		for id, m := range s.Messages {
			var tags []string
			changed := false
			for _, tag := range m.Tags {
				if tag == from {
					changed = true
					if to != "" {
						tags = append(tags, to)
					}
				} else {
					tags = append(tags, tag)
				}
			}
			if changed {
				copy := cloneRecord(m)
				copy.Tags, _ = normalizeTags(tags)
				s.Messages[id] = copy
			}
		}
		return nil
	})
}
func (a *httpAPI) renameTag(w http.ResponseWriter, r *http.Request) {
	var body struct{ Name string }
	if err := decodeJSON(w, r, 1024, &body); err != nil || !tagName.MatchString(body.Name) {
		apiError(w, 400, errors.New("valid Name is required"))
		return
	}
	if err := a.changeTag(r.PathValue("tag"), body.Name); err != nil {
		apiError(w, 500, err)
		return
	}
	w.Write([]byte("ok\n"))
}
func (a *httpAPI) removeTag(w http.ResponseWriter, r *http.Request) {
	if err := a.changeTag(r.PathValue("tag"), ""); err != nil {
		apiError(w, 500, err)
		return
	}
	w.Write([]byte("ok\n"))
}

func (a *httpAPI) listToxics(w http.ResponseWriter, r *http.Request) {
	jsonResponse(w, 200, a.store.toxics.list())
}
func (a *httpAPI) getToxic(w http.ResponseWriter, r *http.Request) {
	for _, t := range a.store.toxics.list() {
		if t.Name == r.PathValue("name") {
			jsonResponse(w, 200, t)
			return
		}
	}
	apiError(w, 404, errors.New("toxic not found"))
}
func (a *httpAPI) createToxic(w http.ResponseWriter, r *http.Request) {
	t := toxic{Enabled: true}
	if err := decodeJSON(w, r, 64<<10, &t); err != nil {
		apiError(w, 400, err)
		return
	}
	t.Hits = 0
	if err := a.store.toxics.put(t, true); err != nil {
		apiError(w, 400, err)
		return
	}
	jsonResponse(w, 201, t)
}
func (a *httpAPI) updateToxic(w http.ResponseWriter, r *http.Request) {
	t := toxic{Enabled: true}
	if err := decodeJSON(w, r, 64<<10, &t); err != nil {
		apiError(w, 400, err)
		return
	}
	if t.Name != "" && t.Name != r.PathValue("name") {
		apiError(w, 400, errors.New("toxic name must match URL"))
		return
	}
	t.Name = r.PathValue("name")
	if err := a.store.toxics.put(t, false); err != nil {
		apiError(w, 400, err)
		return
	}
	a.getToxic(w, r)
}
func (a *httpAPI) toggleToxic(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Enabled *bool `json:"enabled"`
	}
	if err := decodeJSON(w, r, 1024, &body); err != nil || body.Enabled == nil {
		apiError(w, 400, errors.New("enabled is required"))
		return
	}
	registry := a.store.toxics
	registry.mu.Lock()
	t, ok := registry.entries[r.PathValue("name")]
	if ok {
		t.Enabled = *body.Enabled
		registry.entries[t.Name] = t
	}
	registry.mu.Unlock()
	if !ok {
		apiError(w, 404, errors.New("toxic not found"))
		return
	}
	jsonResponse(w, 200, t)
}
func (a *httpAPI) deleteToxic(w http.ResponseWriter, r *http.Request) {
	if !a.store.toxics.remove(r.PathValue("name")) {
		apiError(w, 404, errors.New("toxic not found"))
		return
	}
	w.WriteHeader(204)
}

func (a *httpAPI) folders(w http.ResponseWriter, r *http.Request) {
	folders, err := a.store.user.ListMailboxes(false)
	if err != nil {
		apiError(w, 500, err)
		return
	}
	result := []any{}
	for _, f := range folders {
		status, err := f.Status([]imap.StatusItem{imap.StatusMessages, imap.StatusUidNext, imap.StatusUidValidity})
		if err != nil {
			apiError(w, 500, err)
			return
		}
		result = append(result, map[string]any{"name": f.Name(), "messages": status.Messages, "uid_next": status.UidNext, "uid_validity": status.UidValidity})
	}
	jsonResponse(w, 200, result)
}
func (a *httpAPI) createFolder(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string `json:"name"`
	}
	if err := decodeJSON(w, r, 1024, &body); err != nil {
		apiError(w, 400, err)
		return
	}
	if err := a.store.user.CreateMailbox(body.Name); err != nil {
		apiError(w, 400, err)
		return
	}
	jsonResponse(w, 201, body)
}
func (a *httpAPI) view(w http.ResponseWriter, r *http.Request) {
	file := r.PathValue("file")
	suffix := ".html"
	if strings.HasSuffix(file, ".txt") {
		suffix = ".txt"
	}
	id := strings.TrimSuffix(file, suffix)
	m := a.store.get(id)
	if m == nil {
		apiError(w, 404, errors.New("message not found"))
		return
	}
	if suffix == ".txt" {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Write([]byte(m.Detail.Text))
	} else {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'; style-src 'unsafe-inline'; img-src data:")
		w.Write([]byte(m.Detail.HTML))
	}
}

func (a *httpAPI) release(w http.ResponseWriter, r *http.Request) {
	m := a.requireMessage(w, r)
	if m == nil {
		return
	}
	var body struct{ To []string }
	if err := decodeJSON(w, r, 64<<10, &body); err != nil {
		apiError(w, 400, err)
		return
	}
	if err := relayMessage(r.Context(), a.store.config.Relay, m, body.To, true); err != nil {
		apiError(w, 400, err)
		return
	}
	w.Write([]byte("ok\n"))
}
func (a *httpAPI) importRaw(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, a.store.config.MaxSize))
	if err != nil {
		apiError(w, 400, err)
		return
	}
	folder := r.URL.Query().Get("folder")
	if folder == "" {
		folder = "INBOX"
	}
	m, err := a.store.append(raw, appendOptions{Folder: folder, Notify: true})
	if err != nil {
		apiError(w, 400, err)
		return
	}
	jsonResponse(w, 201, map[string]any{"ID": m.ID, "MessageID": m.Detail.MessageID, "Folder": m.Folder})
}
