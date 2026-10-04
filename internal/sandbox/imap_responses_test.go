package sandbox

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/johlo/go-imap/v2"
	"github.com/johlo/go-imap/v2/imapserver"
)

func TestIMAPMalformedAppendKeepsResponseFraming(t *testing.T) {
	a := testStore(t, defaultConfig())
	w := imapWire(t, imapAddress(t, a.service), mailboxUsername, mailboxPassword)
	if err := w.PrintfLine("a APPEND INBOX {3+}\r\nabc"); err != nil {
		t.Fatal(err)
	}
	if line, err := w.ReadLine(); err != nil || !strings.HasPrefix(line, "a NO ") || strings.ContainsAny(line, "\r\n") {
		t.Fatal("invalid APPEND error reply", line, err)
	}
	if err := w.PrintfLine("b NOOP"); err != nil {
		t.Fatal(err)
	}
	// Do not skip empty or unsolicited lines: they would corrupt a strict client's parser.
	if line, err := w.ReadLine(); err != nil || !strings.HasPrefix(line, "b OK ") {
		t.Fatal("APPEND error leaked an extra response line", line, err)
	}
	if len(a.snapshot()) != 0 {
		t.Fatal("invalid MIME was stored")
	}
}

func TestIMAPMissingDestinationCanBeCreatedAndRetried(t *testing.T) {
	for _, command := range []string{"APPEND", "COPY", "UID COPY"} {
		t.Run(command, func(t *testing.T) {
			a := testStore(t, defaultConfig())
			appendTest(t, a, "source@test")
			w := imapWire(t, imapAddress(t, a.service), mailboxUsername, mailboxPassword)
			imapReply(t, w, "SELECT Sent", "OK")
			request := command + " 1 NewFolder"
			if command == "APPEND" {
				raw := testRaw("append@test", "append", "recipient@example.test")
				request = fmt.Sprintf("APPEND NewFolder {%d+}\r\n%s", len(raw), raw)
			}
			if reply := imapReply(t, w, request, "NO"); !strings.Contains(reply, "a NO [TRYCREATE] ") {
				t.Fatal("missing destination did not suggest CREATE", reply)
			}
			if len(a.snapshot()) != 1 {
				t.Fatal("failed delivery changed storage")
			}
			imapReply(t, w, "CREATE NewFolder", "OK")
			imapReply(t, w, request, "OK")
			if reply := imapReply(t, w, "SELECT NewFolder", "OK"); !strings.Contains(reply, "* 1 EXISTS") {
				t.Fatal("retry did not deliver exactly one message", reply)
			}
			if reply := imapReply(t, w, "SELECT OtherMissingFolder", "NO"); strings.Contains(reply, "[TRYCREATE]") {
				t.Fatal("destination error mapping leaked into SELECT", reply)
			}
		})
	}
}

func TestIMAPSelectFlagsKeepWildcardOnlyInPermanentFlags(t *testing.T) {
	a := testStore(t, defaultConfig())
	w := imapWire(t, imapAddress(t, a.service), mailboxUsername, mailboxPassword)
	for _, command := range []string{"SELECT", "EXAMINE"} {
		reply := imapReply(t, w, command+" Sent", "OK")
		var flags, permanent string
		for _, line := range strings.Split(reply, "\n") {
			if strings.HasPrefix(line, "* FLAGS (") {
				flags = strings.TrimSuffix(strings.TrimPrefix(line, "* FLAGS ("), ")")
			}
			if strings.Contains(line, "[PERMANENTFLAGS (") {
				permanent = line
			}
		}
		if len(strings.Fields(flags)) != 5 || strings.Contains(flags, `\*`) {
			t.Fatal("invalid FLAGS response", reply)
		}
		for _, flag := range []string{`\Seen`, `\Answered`, `\Flagged`, `\Deleted`, `\Draft`} {
			if !hasFlag(strings.Fields(flags), flag) {
				t.Fatal("missing system flag", flag, reply)
			}
		}
		if !strings.Contains(permanent, `\*`) {
			t.Fatal("PERMANENTFLAGS lost keyword support", reply)
		}
	}
}

func TestIMAPErrorConversionPreservesCodesAndSingleLineText(t *testing.T) {
	protocolErr := &imap.Error{Type: imap.StatusResponseTypeBad, Code: imap.ResponseCodeUnavailable, Text: "first\r\nsecond\nthird\rfourth\r\n"}
	for _, input := range []error{errors.New(protocolErr.Text), fmt.Errorf("wrapped: %w", protocolErr)} {
		for _, converted := range []*imap.StatusResponse{imapFaultStatus(input), (*imap.StatusResponse)(imapNo(input).(*imap.Error))} {
			if strings.ContainsAny(converted.Text, "\r\n") || !strings.Contains(converted.Text, "fourth") {
				t.Fatal("invalid status text", converted)
			}
			var original *imap.Error
			if errors.As(input, &original) && (converted.Type != original.Type || converted.Code != original.Code) {
				t.Fatal("error conversion lost protocol metadata", converted)
			}
		}
	}
	if !strings.Contains(protocolErr.Text, "\r\n") {
		t.Fatal("error conversion mutated a shared error")
	}
	for _, input := range []error{nil, fmt.Errorf("handled: %w", imapserver.ErrResponseHandled)} {
		if imapFaultStatus(input) != nil || imapNo(input) != input {
			t.Fatal("error conversion changed suppressed-response handling")
		}
	}
}
