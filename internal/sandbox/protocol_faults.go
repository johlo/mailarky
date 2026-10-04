package sandbox

import (
	"errors"
	"math"
	"regexp"
	"slices"
	"strings"
)

var enhancedCodePattern = regexp.MustCompile(`^[45]\.[0-9]{1,3}\.[0-9]{1,3}$`)
var responseCodePattern = regexp.MustCompile(`^[A-Z][A-Z0-9-]*$`)

func validateFault(t faultRule, scope string) error {
	if strings.TrimSpace(t.Name) == "" || len(t.Name) > 100 || strings.ContainsAny(t.Name, "/\r\n\x00") {
		return errors.New("fault name must be 1-100 characters without slashes or control characters")
	}
	if t.Probability != nil && (math.IsNaN(*t.Probability) || *t.Probability < 0 || *t.Probability > 1) {
		return errors.New("probability must be between 0 and 1")
	}
	if t.Sequence != nil {
		if t.Probability != nil || len(t.Sequence.Steps) == 0 || len(t.Sequence.Steps) > 1024 {
			return errors.New("sequence requires 1-1024 steps and cannot be combined with probability")
		}
		for _, step := range t.Sequence.Steps {
			if step != "pass" && step != "apply" {
				return errors.New("sequence steps must be pass or apply")
			}
		}
	}
	tr, f := t.Trigger, t.Filter
	var commands []string
	switch tr.Protocol {
	case "smtp":
		commands = strings.Fields("CONNECT EHLO HELO AUTH STARTTLS MAIL RCPT DATA BDAT RSET NOOP QUIT VRFY HELP EXPN SEND SOML SAML TURN")
	case "imap":
		commands = append(strings.Fields("CONNECT CAPABILITY NOOP LOGOUT STARTTLS LOGIN AUTHENTICATE SELECT EXAMINE LIST LSUB STATUS CREATE DELETE RENAME SUBSCRIBE UNSUBSCRIBE APPEND CHECK CLOSE EXPUNGE SEARCH FETCH STORE COPY UNSELECT IDLE MOVE"), "UID SEARCH", "UID FETCH", "UID STORE", "UID COPY", "UID MOVE")
	default:
		return errors.New("trigger.protocol must be smtp or imap")
	}
	if !slices.Contains(commands, tr.Command) {
		return errors.New("trigger.command must be a supported uppercase command")
	}
	if !slices.Contains([]string{"before", "content", "after", "active", "notification"}, tr.Phase) {
		return errors.New("trigger.phase must be before, content, after, active or notification")
	}
	if tr.Phase == "active" || tr.Phase == "notification" {
		if tr.Protocol != "imap" || tr.Command != "IDLE" {
			return errors.New("active and notification phases require IMAP IDLE")
		}
		if tr.Phase == "active" {
			if _, ok := t.Action.Parameters.(*disconnectAction); !ok {
				return errors.New("IDLE active phase requires disconnect")
			}
		} else if _, ok := t.Action.Parameters.(*dropAction); !ok {
			return errors.New("IDLE notification phase requires drop")
		}
	}
	if f.Notification != "" && (tr.Phase != "notification" || !slices.Contains([]string{"EXISTS", "EXPUNGE", "FLAGS"}, f.Notification)) {
		return errors.New("notification filter requires IDLE notification phase and EXISTS, EXPUNGE or FLAGS")
	}
	if scope != "server" && scope != "account" {
		return errors.New("invalid registry scope")
	}
	for _, s := range []string{f.Username, f.Folder} {
		if strings.ContainsAny(s, "*?\r\n\x00") {
			return errors.New("filters require exact values")
		}
	}
	if scope == "account" && f.Username != "" {
		return errors.New("username filters belong on server faults; the account endpoint already selects an account")
	}
	if tr.Command == "CONNECT" && (scope != "server" || tr.Phase != "before" || f.Username != "" || f.Folder != "" || f.Message != nil) {
		return errors.New("CONNECT requires server scope, before phase and no filters")
	}
	if scope == "account" && slices.Contains([]string{"EHLO", "HELO", "STARTTLS"}, tr.Command) {
		return errors.New("greeting and TLS negotiation require server scope")
	}
	if tr.Phase == "after" && slices.Contains([]string{"STARTTLS", "AUTH", "LOGIN", "AUTHENTICATE", "QUIT", "LOGOUT"}, tr.Command) {
		return errors.New("authentication, TLS and logout faults require before phase")
	}
	if tr.Phase == "content" && !((tr.Protocol == "smtp" && slices.Contains([]string{"DATA", "BDAT"}, tr.Command)) || (tr.Protocol == "imap" && slices.Contains([]string{"SEARCH", "FETCH", "UID SEARCH", "UID FETCH"}, tr.Command))) {
		return errors.New("content phase requires SMTP DATA/BDAT or IMAP SEARCH/FETCH (including UID)")
	}
	if f.Folder != "" && (tr.Protocol != "imap" || slices.Contains([]string{"CONNECT", "CAPABILITY", "NOOP", "LOGIN", "AUTHENTICATE", "LOGOUT", "STARTTLS", "LIST", "LSUB"}, tr.Command)) {
		return errors.New("this trigger has no folder to match")
	}
	if f.Message != nil {
		s := f.Message
		if s.MessageID == "" && s.From == "" && s.To == "" && len(s.Headers) == 0 {
			return errors.New("message filter requires message_id, from, to or headers")
		}
		if s.MessageID != "" && (strings.Trim(s.MessageID, "<>") == "" || strings.ContainsAny(strings.Trim(s.MessageID, "<>"), "<> \t\r\n")) {
			return errors.New("invalid message_id")
		}
		for _, addr := range []string{s.From, s.To} {
			if addr != "" && !validEnvelope(addr, false) {
				return errors.New("address filters must be exact email addresses")
			}
		}
		for _, v := range []string{s.From, s.To, s.MessageID} {
			if strings.ContainsAny(v, "*?\r\n\x00") {
				return errors.New("message filters require exact values")
			}
		}
		for k, v := range s.Headers {
			if !headerToken.MatchString(k) || v == "" || strings.ContainsAny(v, "*?\r\n\x00") {
				return errors.New("headers require exact names and values")
			}
		}
		if tr.Protocol == "imap" && tr.Phase != "content" {
			return errors.New("IMAP message filters require content phase")
		}
		if tr.Protocol == "smtp" && tr.Command != "MAIL" && tr.Command != "RCPT" && tr.Command != "DATA" && tr.Command != "BDAT" {
			return errors.New("this SMTP command has no message")
		}
		if tr.Protocol == "smtp" && (tr.Command == "MAIL" || tr.Command == "RCPT" || tr.Phase == "before") && (s.MessageID != "" || len(s.Headers) > 0) {
			return errors.New("MIME headers are available only in DATA/BDAT content or after phases")
		}
		if tr.Command == "MAIL" && s.To != "" {
			return errors.New("MAIL has no recipient yet")
		}
	}
	singleLine := func(s string) bool { return len(s) <= 1000 && !strings.ContainsAny(s, "\r\n\x00") }
	switch a := t.Action.Parameters.(type) {
	case *dropAction:
		if tr.Phase != "notification" {
			return errors.New("drop requires IDLE notification phase")
		}
	case *rejectAction:
		if !singleLine(a.Message) {
			return errors.New("rejection message must be a single line of at most 1000 bytes")
		}
		if tr.Protocol == "smtp" {
			if a.Code < 400 || a.Code > 599 || a.Status != "" || a.ResponseCode != "" {
				return errors.New("SMTP rejection requires code 400-599 and no IMAP options")
			}
			if a.EnhancedCode != "" && (!enhancedCodePattern.MatchString(a.EnhancedCode) || int(a.EnhancedCode[0]-'0') != a.Code/100) {
				return errors.New("enhanced_code must match the SMTP reply class")
			}
			if tr.Command == "CONNECT" && a.Code != 421 && a.Code != 554 {
				return errors.New("greeting rejection requires 421 or 554")
			}
		} else {
			if a.Code != 0 || a.EnhancedCode != "" || (a.Status != "NO" && a.Status != "BAD") {
				return errors.New("IMAP rejection requires status NO or BAD and no SMTP options")
			}
			if a.ResponseCode != "" && !responseCodePattern.MatchString(a.ResponseCode) {
				return errors.New("response_code must be an IMAP atom")
			}
			if tr.Command == "CONNECT" {
				return errors.New("use disconnect for an IMAP BYE greeting")
			}
		}
	case *delayAction:
		if a.DelayMS < 0 || a.DelayMS > 30000 {
			return errors.New("delay_ms must be between 0 and 30000")
		}
	case *disconnectAction:
		if a.AfterMS != nil && (tr.Phase != "active" || *a.AfterMS < 0 || *a.AfterMS > 86400000) {
			return errors.New("after_ms requires IDLE active phase and a value from 0 to 86400000")
		}
		if !singleLine(a.Message) {
			return errors.New("disconnect message must be a single line of at most 1000 bytes")
		}
	case *responseAction:
		if a.Response == "" || len(a.Response) > 8192 || strings.ContainsRune(a.Response, 0) {
			return errors.New("response requires 1-8192 bytes without NUL")
		}
	case *capabilitiesAction:
		command := "EHLO"
		if tr.Protocol == "imap" {
			command = "CAPABILITY"
		}
		if tr.Command != command || tr.Phase != "after" || len(a.Omit) == 0 || f.Message != nil {
			return errors.New("capabilities requires EHLO/CAPABILITY after phase and omit tokens")
		}
		for _, v := range a.Omit {
			if v == "" || strings.ContainsAny(v, " \t\r\n\x00") {
				return errors.New("omit requires capability tokens")
			}
		}
	case *resetUIDAction:
		if tr.Protocol != "imap" || scope != "account" || !slices.Contains([]string{"SELECT", "EXAMINE"}, tr.Command) || tr.Phase != "before" || f.Folder == "" || t.MaxHits != 1 {
			return errors.New("reset_uidvalidity requires an account, SELECT/EXAMINE before, an exact folder and max_hits: 1")
		}
	case *hideAction, *replaceHeaderAction, *replaceBodyAction:
		if tr.Protocol != "imap" || tr.Phase != "content" || f.Message == nil {
			return errors.New("message transformations require IMAP content phase and a message filter")
		}
		if a, ok := a.(*replaceHeaderAction); ok && (!headerToken.MatchString(a.Header) || strings.ContainsAny(a.Value, "\r\n\x00")) {
			return errors.New("replace_header requires a valid name and single-line value")
		}
	default:
		return errors.New("action is required")
	}
	return nil
}

func filterCapabilities(caps, omit []string) []string {
	out := make([]string, 0, len(caps))
	for _, cap := range caps {
		fields := strings.Fields(cap)
		if len(fields) == 0 {
			continue
		}
		token := strings.ToUpper(fields[0])
		remove := false
		for _, v := range omit {
			v = strings.ToUpper(v)
			if token == v || strings.HasPrefix(token, v+"=") {
				remove = true
			}
		}
		if !remove {
			out = append(out, cap)
		}
	}
	return out
}
