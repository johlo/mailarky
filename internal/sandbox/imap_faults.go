package sandbox

import (
	"crypto/tls"
	"errors"
	"strings"
	"time"

	"github.com/johlo/go-imap/v2"
	"github.com/johlo/go-imap/v2/imapserver"
)

func imapEvent(c *imapserver.Conn, cmd *imapserver.Command, phase string) protocolEvent {
	e := protocolEvent{protocol: "imap", command: cmd.Name, phase: phase}
	if s, ok := c.Session().(*imapSession); ok {
		if s.account != nil {
			e.username = s.account.identity.Username
		}
		if s.selected != nil {
			e.folder = s.selected.name
		}
	}
	switch cmd.Name {
	case "SELECT", "EXAMINE", "STATUS", "CREATE", "DELETE", "RENAME", "SUBSCRIBE", "UNSUBSCRIBE", "APPEND":
		e.folder = cmd.Mailbox
	case "LIST", "LSUB", "LOGIN", "AUTHENTICATE", "CAPABILITY", "NOOP", "LOGOUT", "STARTTLS", "CONNECT":
		e.folder = ""
	}
	return e
}
func newIMAPServer(service *Service) *imapserver.Server {
	options := &imapserver.Options{
		Caps: imap.CapSet{imap.CapIMAP4rev1: {}},
		NewSession: func(c *imapserver.Conn) (imapserver.Session, *imapserver.GreetingData, error) {
			return &imapSession{service: service, conn: c}, nil, nil
		},
	}
	options.GreetingHook = func(c *imapserver.Conn) error {
		// tls.Listener accepts connections before negotiating TLS. Greeting
		// faults belong after the handshake, so a silent close stays an IMAP
		// failure and a banner delay does not consume the TLS handshake timeout.
		if conn, ok := c.NetConn().(*tls.Conn); ok {
			if err := conn.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
				_ = c.Close()
				return imapserver.ErrResponseHandled
			}
			if err := conn.Handshake(); err != nil {
				_ = c.Close()
				return imapserver.ErrResponseHandled
			}
			if err := conn.SetDeadline(time.Time{}); err != nil {
				_ = c.Close()
				return imapserver.ErrResponseHandled
			}
		}
		cmd := &imapserver.Command{Name: "CONNECT"}
		return c.Session().(*imapSession).apply(cmd, imapEvent(c, cmd, "before"), nil)
	}
	options.CommandHook = func(c *imapserver.Conn, cmd *imapserver.Command) error {
		s := c.Session().(*imapSession)
		e := imapEvent(c, cmd, "before")
		cmd.HookData = e
		if cmd.Name == "IDLE" && s.selected == nil {
			return imapNo(errors.New("select a mailbox before IDLE"))
		}
		return s.apply(cmd, e, s.account)
	}
	options.ResponseHook = func(c *imapserver.Conn, cmd *imapserver.Command, res *imap.StatusResponse) *imap.StatusResponse {
		s := c.Session().(*imapSession)
		if fault := s.pendingFault; fault != nil {
			s.pendingFault = nil
			folder := ""
			if s.selected != nil {
				folder = s.selected.name
			}
			return imapFaultStatus(applyIMAPFault(c, cmd, service, s.account, *fault, folder))
		}
		if res == nil || res.Type != imap.StatusResponseTypeOK {
			return res
		}
		e, ok := cmd.HookData.(protocolEvent)
		if !ok {
			e = imapEvent(c, cmd, "before")
		}
		e.phase = "after"
		if err := s.apply(cmd, e, s.account); err != nil {
			return imapFaultStatus(err)
		}
		return res
	}
	options.CapabilitiesHook = func(c *imapserver.Conn, caps []imap.Cap) []imap.Cap {
		s := c.Session().(*imapSession)
		e := imapEvent(c, &imapserver.Command{Name: "CAPABILITY"}, "after")
		rules := service.serverFaults.claim(e, true)
		if s.account != nil {
			rules = append(rules, s.account.faults.claim(e, true)...)
		}
		list := make([]string, len(caps))
		for i, cap := range caps {
			list[i] = string(cap)
		}
		for _, rule := range rules {
			list = filterCapabilities(list, rule.Action.Parameters.(*capabilitiesAction).Omit)
		}
		caps = make([]imap.Cap, len(list))
		for i, cap := range list {
			caps[i] = imap.Cap(cap)
		}
		return caps
	}
	return imapserver.New(options)
}
func (s *imapSession) apply(cmd *imapserver.Command, e protocolEvent, account *Account) error {
	for _, rule := range s.service.serverFaults.claim(e, false) {
		if err := applyIMAPFault(s.conn, cmd, s.service, account, rule, e.folder); err != nil {
			return err
		}
	}
	if account != nil {
		e.identityOnly = false
		for _, rule := range account.faults.claim(e, false) {
			if err := applyIMAPFault(s.conn, cmd, s.service, account, rule, e.folder); err != nil {
				return err
			}
		}
	}
	return nil
}
func imapFaultStatus(err error) *imap.StatusResponse {
	if err == nil || errors.Is(err, imapserver.ErrResponseHandled) {
		return nil
	}
	status := imap.StatusResponse{Type: imap.StatusResponseTypeNo, Text: err.Error()}
	var protocolErr *imap.Error
	if errors.As(err, &protocolErr) {
		status = imap.StatusResponse(*protocolErr)
	}
	// Internal errors can contain client-supplied MIME text; status text must stay on one line.
	status.Text = strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(status.Text, "\r", " "), "\n", " "))
	return &status
}
func wireResponse(response, tag string) string {
	response = strings.ReplaceAll(response, "{tag}", tag)
	if !strings.HasSuffix(response, "\r\n") {
		response += "\r\n"
	}
	return response
}
func imapDisconnect(c *imapserver.Conn, message string, silent bool) error {
	if !silent {
		_ = c.Bye(message)
	} else {
		_ = c.Close()
	}
	return imapserver.ErrResponseHandled
}
func applyIMAPFault(c *imapserver.Conn, cmd *imapserver.Command, service *Service, account *Account, rule faultRule, folder string) error {
	switch a := rule.Action.Parameters.(type) {
	case *delayAction:
		done := service.ctx.Done()
		if account != nil {
			done = account.done
		}
		return waitFault(service.ctx, done, a.DelayMS)
	case *rejectAction:
		return &imap.Error{Type: imap.StatusResponseType(a.Status), Code: imap.ResponseCode(a.ResponseCode), Text: faultMessage(rule.Name, a.Message)}
	case *disconnectAction:
		return imapDisconnect(c, faultMessage(rule.Name, a.Message), a.Silent)
	case *responseAction:
		if err := c.WriteRawResponse(wireResponse(a.Response, cmd.Tag)); err != nil {
			return err
		}
		if a.Close || cmd.Name == "CONNECT" {
			_ = c.Close()
		}
		return imapserver.ErrResponseHandled
	case *resetUIDAction:
		return account.resetUIDValidity(folder)
	}
	return nil
}
func (b *Account) resetUIDValidity(folder string) error {
	if strings.EqualFold(folder, "INBOX") {
		folder = "INBOX"
	}
	return b.mutate(func(state *storeState) error {
		f, ok := state.Folders[folder]
		if !ok {
			return errNoSuchMailbox
		}
		if f.Validity == ^uint32(0) {
			return errors.New("UIDVALIDITY exhausted")
		}
		f.Validity++
		state.Folders[folder] = f
		return nil
	})
}
