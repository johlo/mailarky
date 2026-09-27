package emulator

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/mail"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Selectors are conjunctive and exact (addresses are case-insensitive). Empty
// selectors and wildcard patterns are rejected: a test cannot poison the server.
type messageSelector struct {
	MessageID string            `json:"message_id,omitempty"`
	From      string            `json:"from,omitempty"`
	To        string            `json:"to,omitempty"`
	Headers   map[string]string `json:"headers,omitempty"`
	Folder    string            `json:"folder,omitempty"`
}

type toxic struct {
	Name        string          `json:"name"`
	Type        string          `json:"type"`
	Enabled     bool            `json:"enabled"`
	Selector    messageSelector `json:"selector"`
	Attributes  toxicAttributes `json:"attributes"`
	Probability *float64        `json:"probability,omitempty"`
	MaxHits     uint64          `json:"max_hits,omitempty"`
	Hits        uint64          `json:"hits"`
}

type toxicAttributes struct {
	Stage   string `json:"stage,omitempty"`
	Code    int    `json:"code,omitempty"`
	DelayMS int    `json:"delay_ms,omitempty"`
	Header  string `json:"header,omitempty"`
	Value   string `json:"value,omitempty"`
}

type toxicRegistry struct {
	mu      sync.Mutex
	entries map[string]toxic
	order   []string
}

func newToxicRegistry() *toxicRegistry { return &toxicRegistry{entries: map[string]toxic{}} }

func validateToxic(t toxic) error {
	if strings.TrimSpace(t.Name) == "" || len(t.Name) > 100 || strings.ContainsAny(t.Name, "/\r\n") {
		return errors.New("toxic name must be 1-100 characters without slashes or newlines")
	}
	s := t.Selector
	if s.MessageID != "" && (strings.Trim(s.MessageID, "<>") == "" || strings.ContainsAny(strings.Trim(s.MessageID, "<>"), "<> \t")) {
		return errors.New("message_id must identify a nonempty Message-ID")
	}
	for _, address := range []string{s.From, s.To} {
		if address != "" && !validEnvelope(address, false) {
			return errors.New("address selectors must be exact envelope addresses")
		}
	}
	if s.MessageID == "" && s.From == "" && s.To == "" && len(s.Headers) == 0 {
		return errors.New("a message_id, from, to, or exact header selector is required; folder alone is not a message selector")
	}
	for _, value := range []string{s.MessageID, s.From, s.To, s.Folder} {
		if strings.ContainsAny(value, "*?\r\n") {
			return errors.New("wildcards and newlines are not allowed in selectors")
		}
	}
	for key, value := range s.Headers {
		if !headerToken.MatchString(key) || value == "" || strings.ContainsAny(key+value, "*?\r\n") {
			return errors.New("header selectors require nonempty exact names and values")
		}
	}
	if t.Probability != nil && (*t.Probability < 0 || *t.Probability > 1) {
		return errors.New("probability must be between 0 and 1")
	}
	if t.Attributes.DelayMS < 0 || t.Attributes.DelayMS > 30000 {
		return errors.New("delay_ms must be between 0 and 30000")
	}
	switch t.Type {
	case "smtp_reject":
		if t.Attributes.Code < 400 || t.Attributes.Code > 599 {
			return errors.New("smtp_reject requires a 400-599 code")
		}
	case "smtp_delay", "imap_delay", "imap_hide":
	case "imap_replace_header":
		if !headerToken.MatchString(t.Attributes.Header) || strings.ContainsAny(t.Attributes.Value, "\r\n") {
			return errors.New("imap_replace_header requires a valid header name and a single-line value")
		}
	default:
		return fmt.Errorf("unsupported toxic type %q", t.Type)
	}
	if strings.HasPrefix(t.Type, "smtp_") {
		stage := t.Attributes.Stage
		if stage == "" {
			stage = "data"
		}
		if stage != "sender" && stage != "recipient" && stage != "data" {
			return errors.New("SMTP stage must be sender, recipient, or data")
		}
		if stage != "data" && (s.MessageID != "" || len(s.Headers) > 0 || s.Folder != "") {
			return errors.New("pre-DATA toxics can only select envelope addresses")
		}
		if stage == "sender" && (s.From == "" || s.To != "") {
			return errors.New("sender-stage toxics require only an exact envelope sender")
		}
	} else if t.Attributes.Stage != "" {
		return errors.New("stage is only valid for SMTP toxics")
	}
	return nil
}

var headerToken = regexp.MustCompile(`^[A-Za-z0-9!#$%&'*+.^_\x60|~-]+$`)

func (s messageSelector) matches(m *storedMessage) bool {
	if s.MessageID != "" && strings.Trim(s.MessageID, "<>") != m.Detail.MessageID {
		return false
	}
	if s.Folder != "" && s.Folder != m.Folder {
		return false
	}
	if s.From != "" && !strings.EqualFold(s.From, m.EnvelopeFrom) && !(m.EnvelopeFrom == "" && m.Detail.From != nil && strings.EqualFold(s.From, m.Detail.From.Address)) {
		return false
	}
	if s.To != "" {
		found := false
		for _, to := range m.EnvelopeTo {
			if strings.EqualFold(to, s.To) {
				found = true
			}
		}
		if len(m.EnvelopeTo) == 0 {
			for _, list := range [][]*mail.Address{m.Detail.To, m.Detail.Cc, m.Detail.Bcc} {
				for _, to := range list {
					if strings.EqualFold(to.Address, s.To) {
						found = true
					}
				}
			}
		}
		if !found {
			return false
		}
	}
	for key, value := range s.Headers {
		found := false
		for actual, values := range m.Headers {
			if strings.EqualFold(key, actual) {
				for _, v := range values {
					if v == value {
						found = true
					}
				}
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func (r *toxicRegistry) list() []toxic {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]toxic, 0, len(r.entries))
	for _, name := range r.order {
		if t, ok := r.entries[name]; ok {
			out = append(out, t)
		}
	}
	return out
}
func (r *toxicRegistry) put(t toxic, create bool) error {
	if err := validateToxic(t); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	old, exists := r.entries[t.Name]
	if create && exists {
		return errors.New("toxic name already exists")
	}
	if !exists {
		t.Hits = 0
		r.order = append(r.order, t.Name)
	} else {
		t.Hits = old.Hits
	}
	r.entries[t.Name] = t
	return nil
}
func (r *toxicRegistry) remove(name string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.entries[name]
	delete(r.entries, name)
	for i, n := range r.order {
		if n == name {
			r.order = append(r.order[:i], r.order[i+1:]...)
			break
		}
	}
	return ok
}

// Claim matching faults atomically, then perform delays outside every mailbox
// and registry lock. Parallel tests with different selectors never wait on them.
func (r *toxicRegistry) claim(protocol, stage string, m *storedMessage) []toxic {
	r.mu.Lock()
	defer r.mu.Unlock()
	var matches []toxic
	for _, name := range r.order {
		t, ok := r.entries[name]
		if !ok || !t.Enabled || !strings.HasPrefix(t.Type, protocol+"_") || (t.MaxHits > 0 && t.Hits >= t.MaxHits) {
			continue
		}
		if protocol == "smtp" {
			want := t.Attributes.Stage
			if want == "" {
				want = "data"
			}
			if want != stage {
				continue
			}
		}
		if !t.Selector.matches(m) {
			continue
		}
		if t.Probability != nil && rand.Float64() >= *t.Probability {
			continue
		}
		t.Hits++
		r.entries[name] = t
		matches = append(matches, t)
	}
	return matches
}

func waitToxic(ctx context.Context, done <-chan struct{}, milliseconds int) error {
	if milliseconds == 0 {
		return nil
	}
	timer := time.NewTimer(time.Duration(milliseconds) * time.Millisecond)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-done:
		return errors.New("mail server stopped")
	}
}
