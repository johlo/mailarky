package sandbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"regexp"
	"strings"
	"sync"
	"time"
)

type messageSelector struct {
	MessageID string            `json:"message_id,omitempty"`
	From      string            `json:"from,omitempty"`
	To        string            `json:"to,omitempty"`
	Headers   map[string]string `json:"headers,omitempty"`
}
type faultTrigger struct {
	Protocol string `json:"protocol"`
	Command  string `json:"command"`
	Phase    string `json:"phase"`
}
type faultFilter struct {
	Username     string           `json:"username,omitempty"`
	Folder       string           `json:"folder,omitempty"`
	Message      *messageSelector `json:"message,omitempty"`
	Notification string           `json:"notification,omitempty"`
}
type faultSequence struct {
	Steps  []string `json:"steps"`
	Repeat bool     `json:"repeat,omitempty"`
}
type faultRule struct {
	Name        string         `json:"name"`
	Enabled     bool           `json:"enabled"`
	Trigger     faultTrigger   `json:"trigger"`
	Filter      faultFilter    `json:"filter"`
	Action      faultAction    `json:"action"`
	Probability *float64       `json:"probability,omitempty"`
	MaxHits     uint64         `json:"max_hits,omitempty"`
	Hits        uint64         `json:"hits"`
	Matches     uint64         `json:"matches"`
	Sequence    *faultSequence `json:"sequence,omitempty"`
	ExpiresAt   *time.Time     `json:"expires_at,omitempty"`
}

// Each action has its own strict JSON schema. Irrelevant options are rejected.
type faultAction struct{ Parameters any }
type rejectAction struct {
	Type         string `json:"type"`
	Code         int    `json:"code,omitempty"`
	EnhancedCode string `json:"enhanced_code,omitempty"`
	Status       string `json:"status,omitempty"`
	ResponseCode string `json:"response_code,omitempty"`
	Message      string `json:"message,omitempty"`
}
type delayAction struct {
	Type    string `json:"type"`
	DelayMS int    `json:"delay_ms"`
}
type disconnectAction struct {
	Type    string `json:"type"`
	Silent  bool   `json:"silent,omitempty"`
	Message string `json:"message,omitempty"`
	AfterMS *int   `json:"after_ms,omitempty"`
}
type dropAction struct {
	Type string `json:"type"`
}
type responseAction struct {
	Type     string `json:"type"`
	Response string `json:"response"`
	Close    bool   `json:"close,omitempty"`
}
type capabilitiesAction struct {
	Type string   `json:"type"`
	Omit []string `json:"omit"`
}
type hideAction struct {
	Type string `json:"type"`
}
type resetUIDAction struct {
	Type string `json:"type"`
}
type replaceHeaderAction struct {
	Type   string `json:"type"`
	Header string `json:"header"`
	Value  string `json:"value"`
}
type replaceBodyAction struct {
	Type  string `json:"type"`
	Value string `json:"value"`
}

func (a faultAction) MarshalJSON() ([]byte, error) { return json.Marshal(a.Parameters) }
func (a *faultAction) UnmarshalJSON(data []byte) error {
	var tag struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(data, &tag); err != nil {
		return err
	}
	switch tag.Type {
	case "reject":
		a.Parameters = &rejectAction{}
	case "delay":
		a.Parameters = &delayAction{}
	case "disconnect":
		a.Parameters = &disconnectAction{}
	case "drop":
		a.Parameters = &dropAction{}
	case "response":
		a.Parameters = &responseAction{}
	case "capabilities":
		a.Parameters = &capabilitiesAction{}
	case "hide":
		a.Parameters = &hideAction{}
	case "reset_uidvalidity":
		a.Parameters = &resetUIDAction{}
	case "replace_header":
		a.Parameters = &replaceHeaderAction{}
	case "replace_body":
		a.Parameters = &replaceBodyAction{}
	default:
		return fmt.Errorf("unknown action type %q", tag.Type)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	for name, value := range fields {
		if strings.TrimSpace(string(value)) == "null" {
			return fmt.Errorf("action.%s cannot be null", name)
		}
	}
	var required []string
	switch tag.Type {
	case "delay":
		required = []string{"delay_ms"}
	case "response":
		required = []string{"response"}
	case "capabilities":
		required = []string{"omit"}
	case "replace_header":
		required = []string{"header", "value"}
	case "replace_body":
		required = []string{"value"}
	}
	for _, name := range required {
		if _, ok := fields[name]; !ok {
			return fmt.Errorf("action.%s is required", name)
		}
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	return decoder.Decode(a.Parameters)
}

type faultRegistry struct {
	mu      sync.Mutex
	entries map[string]faultRule
	order   []string
}

func newFaultRegistry() *faultRegistry { return &faultRegistry{entries: map[string]faultRule{}} }

var headerToken = regexp.MustCompile("^[A-Za-z0-9!#$%&'*+.^_`|~-]+$")

func (s messageSelector) matches(m *storedMessage) bool {
	if m == nil {
		return false
	}
	if s.MessageID != "" && strings.Trim(s.MessageID, "<>") != m.Detail.MessageID {
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
			for _, list := range [][]*address{m.Detail.To, m.Detail.Cc, m.Detail.Bcc} {
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
func (r *faultRegistry) list() []faultRule {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]faultRule, 0, len(r.entries))
	for _, name := range r.order {
		out = append(out, r.entries[name])
	}
	return out
}
func (r *faultRegistry) get(name string) (faultRule, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	t, ok := r.entries[name]
	return t, ok
}
func (r *faultRegistry) put(t faultRule, create bool, scope string) error {
	if err := validateFault(t, scope); err != nil {
		return err
	}
	// Own all maps, slices and action parameters rather than sharing caller memory.
	raw, err := json.Marshal(t)
	if err != nil {
		return err
	}
	if err = json.Unmarshal(raw, &t); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	old, exists := r.entries[t.Name]
	if create && exists {
		return errors.New("fault name already exists")
	}
	if !exists {
		t.Hits = 0
		t.Matches = 0
		r.order = append(r.order, t.Name)
	} else {
		t.Hits = old.Hits
		t.Matches = old.Matches
	}
	r.entries[t.Name] = t
	return nil
}
func (r *faultRegistry) toggle(name string, enabled bool) (faultRule, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	t, ok := r.entries[name]
	if ok {
		t.Enabled = enabled
		r.entries[name] = t
	}
	return t, ok
}
func (r *faultRegistry) remove(name string) bool {
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

type protocolEvent struct {
	protocol, command, phase, username, folder string
	notification                               string
	message                                    *storedMessage
	identityOnly                               bool
}

// Claim under one lock; execute actions after releasing all store/registry locks.
func (r *faultRegistry) claim(e protocolEvent, capabilities bool) []faultRule {
	r.mu.Lock()
	defer r.mu.Unlock()
	var result []faultRule
	now := time.Now()
	for _, name := range r.order {
		t := r.entries[name]
		if !t.Enabled || (t.MaxHits > 0 && t.Hits >= t.MaxHits) || (t.ExpiresAt != nil && !now.Before(*t.ExpiresAt)) {
			continue
		}
		_, isCaps := t.Action.Parameters.(*capabilitiesAction)
		if isCaps != capabilities {
			continue
		}
		if t.Trigger.Protocol != e.protocol || t.Trigger.Command != e.command || t.Trigger.Phase != e.phase {
			continue
		}
		f := t.Filter
		if e.identityOnly && f.Username == "" {
			continue
		}
		if f.Username != "" && f.Username != e.username {
			continue
		}
		if f.Folder != "" && f.Folder != e.folder && !(strings.EqualFold(f.Folder, "INBOX") && strings.EqualFold(e.folder, "INBOX")) {
			continue
		}
		if f.Message != nil && !f.Message.matches(e.message) {
			continue
		}
		if f.Notification != "" && f.Notification != e.notification {
			continue
		}
		t.Matches++
		r.entries[name] = t
		if t.Sequence != nil {
			index := t.Matches - 1
			if t.Sequence.Repeat {
				index %= uint64(len(t.Sequence.Steps))
			}
			if index >= uint64(len(t.Sequence.Steps)) || t.Sequence.Steps[index] != "apply" {
				continue
			}
		}
		if t.Probability != nil && rand.Float64() >= *t.Probability {
			continue
		}
		t.Hits++
		r.entries[name] = t
		result = append(result, t)
	}
	return result
}
func faultMessage(name, message string) string {
	if message != "" {
		return message
	}
	return "Injected fault: " + name
}
func waitFault(ctx context.Context, done <-chan struct{}, milliseconds int) error {
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
