package sandbox

import (
	"fmt"

	"github.com/johlo/go-imap/v2"
	"github.com/johlo/go-smtp"
)

func smtpRecorder(c *smtp.Conn) *commandRecorder {
	if s, ok := c.Session().(*smtpSession); ok {
		return s.recorder
	}
	return nil
}

func recordSMTPReply(c *smtp.Conn, reply *smtp.Reply, closed bool) {
	if reply == nil || reply.Code < 200 || reply.Code >= 300 && reply.Code < 400 {
		return // SASL challenges and DATA continuations are not completions.
	}
	response := fmt.Sprint(reply.Code)
	enhanced := reply.EnhancedCode
	if enhanced == smtp.EnhancedCodeNotSet {
		enhanced = smtp.EnhancedCode{reply.Code / 100, 0, 0}
	}
	if enhanced != smtp.NoEnhancedCode {
		response += fmt.Sprintf(" %d.%d.%d", enhanced[0], enhanced[1], enhanced[2])
	}
	outcome := "response"
	if closed {
		outcome = "disconnect"
	}
	smtpRecorder(c).finish(outcome, response, closed || c.Command() == "QUIT" && reply.Code == 221)
}

func (s *imapSession) recordReply(reply *imap.StatusResponse) {
	if reply == nil {
		return
	}
	response := string(reply.Type)
	// Structured response codes carry no free-form reply text or SASL data.
	if code := string(reply.Code); len(code) <= 64 && responseCodePattern.MatchString(code) {
		response += " [" + code + "]"
	}
	s.recorder.finish("response", response, s.conn.Command().Name == "LOGOUT" && reply.Type == imap.StatusResponseTypeOK)
}
