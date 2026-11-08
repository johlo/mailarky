package sandbox

import (
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

const commandHistoryCapacity = 1000

// History contains protocol summaries, never raw arguments, SASL exchanges,
// message literals, or reply text (which can echo client-supplied secrets).
type commandHistoryEntry struct {
	ID               uint64    `json:"id"`
	Protocol         string    `json:"protocol"`
	ConnectionID     string    `json:"connection_id"`
	Command          string    `json:"command"`
	StartedAt        time.Time `json:"started_at"`
	CompletedAt      time.Time `json:"completed_at"`
	DurationMS       float64   `json:"duration_ms"`
	Outcome          string    `json:"outcome"`
	Response         string    `json:"response"`
	ConnectionClosed bool      `json:"connection_closed"`
	Mailbox          string    `json:"mailbox,omitempty"`
	Recipient        string    `json:"recipient,omitempty"`
}

type commandHistory struct {
	mu         sync.Mutex
	entries    []commandHistoryEntry
	head       int
	nextID     uint64
	generation uint64
	dropped    uint64
	clearedAt  time.Time
}

func (h *commandHistory) epoch() uint64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.generation
}

func (h *commandHistory) append(entry commandHistoryEntry, generation uint64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if generation != h.generation || entry.StartedAt.Before(h.clearedAt) {
		return // Clearing history also discards commands already in progress.
	}
	h.nextID++
	entry.ID = h.nextID
	if len(h.entries) < commandHistoryCapacity {
		h.entries = append(h.entries, entry)
	} else {
		h.entries[h.head] = entry
		h.head = (h.head + 1) % commandHistoryCapacity
		h.dropped++
	}
}

func (h *commandHistory) clear() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.entries, h.head, h.dropped = nil, 0, 0
	h.generation++
	h.clearedAt = time.Now().UTC()
}

type historyFilter struct {
	protocol, command, connectionID string
	after                           uint64
	limit                           int
}

type commandHistoryPage struct {
	Commands  []commandHistoryEntry `json:"commands"`
	NextAfter uint64                `json:"next_after"`
	HasMore   bool                  `json:"has_more"`
	Dropped   uint64                `json:"dropped"`
	Capacity  int                   `json:"capacity"`
}

func (h *commandHistory) page(f historyFilter) commandHistoryPage {
	h.mu.Lock()
	defer h.mu.Unlock()
	p := commandHistoryPage{Commands: []commandHistoryEntry{}, NextAfter: max(f.after, h.nextID), Dropped: h.dropped, Capacity: commandHistoryCapacity}
	for i := range len(h.entries) {
		e := h.entries[(h.head+i)%len(h.entries)]
		if e.ID <= f.after || f.protocol != "" && f.protocol != e.Protocol || f.command != "" && f.command != e.Command || f.connectionID != "" && f.connectionID != e.ConnectionID {
			continue
		}
		if len(p.Commands) == f.limit {
			p.HasMore = true
			p.NextAfter = p.Commands[len(p.Commands)-1].ID
			break
		}
		p.Commands = append(p.Commands, e)
	}
	return p
}

func (a *accountAPI) commandHistory(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodDelete {
		a.account.history.clear()
		w.WriteHeader(http.StatusNoContent)
		return
	}
	q := r.URL.Query()
	f := historyFilter{protocol: q.Get("protocol"), command: strings.ToUpper(q.Get("command")), connectionID: q.Get("connection_id"), limit: 100}
	if f.protocol != "" && f.protocol != "smtp" && f.protocol != "imap" {
		apiError(w, 400, errors.New("protocol must be smtp or imap"))
		return
	}
	if raw, exists := q["after"]; exists {
		var err error
		f.after, err = strconv.ParseUint(raw[0], 10, 64)
		if err != nil {
			apiError(w, 400, errors.New("after must be a nonnegative integer"))
			return
		}
	}
	if raw, exists := q["limit"]; exists {
		var err error
		f.limit, err = strconv.Atoi(raw[0])
		if err != nil || f.limit < 1 || f.limit > commandHistoryCapacity {
			apiError(w, 400, errors.New("limit must be between 1 and 1000"))
			return
		}
	}
	jsonResponse(w, 200, a.account.history.page(f))
}

type commandAttempt struct {
	entry      commandHistoryEntry
	started    time.Time
	account    *Account
	generation uint64
}

// Protocol callbacks are mostly sequential, but IDLE and SMTP shutdown can
// finish a command from another goroutine. Exactly one completion is recorded.
type commandRecorder struct {
	mu           sync.Mutex
	connectionID string
	current      *commandAttempt
}

func newCommandRecorder() *commandRecorder {
	return &commandRecorder{connectionID: uuid.NewString()}
}

func (r *commandRecorder) start(protocol, command string, account *Account) {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	r.current = &commandAttempt{started: now, entry: commandHistoryEntry{Protocol: protocol, ConnectionID: r.connectionID, Command: historyCommand(protocol, command), StartedAt: now.UTC()}}
	r.attachLocked(account)
}

func (r *commandRecorder) attach(account *Account) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.attachLocked(account)
}

func (r *commandRecorder) attachLocked(account *Account) {
	if r.current != nil && r.current.account == nil && account != nil {
		r.current.account = account
		r.current.generation = account.history.epoch()
	}
}

func (r *commandRecorder) details(mailbox, recipient string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.current != nil {
		r.current.entry.Mailbox = historyDetail(mailbox)
		r.current.entry.Recipient = historyDetail(recipient)
	}
}

func (r *commandRecorder) finish(outcome, response string, closed bool) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	a := r.current
	r.current = nil
	if a == nil || a.account == nil {
		return
	}
	e := a.entry
	now := time.Now()
	e.CompletedAt = now.UTC()
	e.DurationMS = float64(now.Sub(a.started)) / float64(time.Millisecond)
	e.Outcome, e.Response, e.ConnectionClosed = outcome, response, closed
	a.account.history.append(e, a.generation)
}

func historyDetail(s string) string {
	if len(s) > 256 {
		return s[:256]
	}
	return s
}

func historyCommand(protocol, command string) string {
	commands := "EHLO HELO AUTH STARTTLS MAIL RCPT DATA BDAT RSET NOOP QUIT VRFY HELP EXPN SEND SOML SAML TURN"
	if protocol == "imap" {
		commands = "CAPABILITY NOOP LOGOUT STARTTLS LOGIN AUTHENTICATE SELECT EXAMINE LIST LSUB STATUS CREATE DELETE RENAME SUBSCRIBE UNSUBSCRIBE APPEND CHECK CLOSE EXPUNGE SEARCH FETCH STORE COPY UNSELECT IDLE MOVE GETMETADATA SETMETADATA ENABLE NAMESPACE UNAUTHENTICATE"
		if strings.HasPrefix(command, "UID ") && slices.Contains(strings.Fields("SEARCH FETCH STORE COPY MOVE EXPUNGE"), strings.TrimPrefix(command, "UID ")) {
			return command
		}
	}
	if slices.Contains(strings.Fields(commands), command) {
		return command
	}
	return "UNKNOWN" // Unknown command names can themselves contain credentials.
}
