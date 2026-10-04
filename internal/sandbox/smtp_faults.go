package sandbox

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/johlo/go-smtp"
)

type smtpInjectedError struct{ rule faultRule }

func (e *smtpInjectedError) Error() string { return "Injected fault: " + e.rule.Name }
func applySMTPFault(c *smtp.Conn, service *Service, t faultRule) bool {
	switch a := t.Action.Parameters.(type) {
	case *delayAction:
		if err := waitFault(service.ctx, service.ctx.Done(), a.DelayMS); err != nil {
			_ = c.Close()
			return true
		}
		return false
	case *rejectAction:
		enhanced := smtp.EnhancedCodeNotSet
		if a.EnhancedCode != "" {
			_, _ = fmt.Sscanf(a.EnhancedCode, "%d.%d.%d", &enhanced[0], &enhanced[1], &enhanced[2])
		}
		c.WriteReply(&smtp.Reply{Code: a.Code, EnhancedCode: enhanced, Lines: []string{faultMessage(t.Name, a.Message)}})
		if a.Code == 421 || c.Command() == "CONNECT" {
			_ = c.Close()
		}
		return true
	case *disconnectAction:
		if !a.Silent {
			c.WriteReply(&smtp.Reply{Code: 421, EnhancedCode: smtp.EnhancedCode{4, 3, 2}, Lines: []string{faultMessage(t.Name, a.Message)}})
		}
		_ = c.Close()
		return true
	case *responseAction:
		_, _ = io.WriteString(c.Conn(), wireResponse(a.Response, ""))
		if a.Close || c.Command() == "CONNECT" {
			_ = c.Close()
		}
		return true
	}
	return false
}
func (s *smtpSession) faults(command, phase string, account *Account, message *storedMessage, server bool) error {
	e := protocolEvent{protocol: "smtp", command: command, phase: phase, username: s.username, message: message}
	var rules []faultRule
	if server {
		rules = s.backend.service.serverFaults.claim(e, false)
	}
	if account != nil {
		rules = append(rules, account.faults.claim(e, false)...)
	}
	for _, t := range rules {
		switch a := t.Action.Parameters.(type) {
		case *delayAction:
			done := s.backend.service.ctx.Done()
			if account != nil {
				done = account.done
			}
			if err := waitFault(context.Background(), done, a.DelayMS); err != nil {
				return err
			}
		default:
			if account != nil {
				account.rejected.Add(1)
			}
			return &smtpInjectedError{t}
		}
	}
	return nil
}
func (s *smtpSession) authenticationFault(username string) error {
	e := protocolEvent{protocol: "smtp", command: "AUTH", phase: "before", username: username, identityOnly: true}
	rules := s.backend.service.serverFaults.claim(e, false)
	if account := s.backend.service.accountByUsername(username); account != nil {
		e.identityOnly = false
		rules = append(rules, account.faults.claim(e, false)...)
	}
	for _, t := range rules {
		if a, ok := t.Action.Parameters.(*delayAction); ok {
			if err := waitFault(s.backend.service.ctx, s.backend.service.ctx.Done(), a.DelayMS); err != nil {
				return err
			}
		} else {
			return &smtpInjectedError{t}
		}
	}
	return nil
}

type smtpCommandContext struct {
	username string
	accounts []*smtpDelivery
	message  *storedMessage
}

func smtpContext(c *smtp.Conn) smtpCommandContext {
	session, ok := c.Session().(*smtpSession)
	if !ok {
		return smtpCommandContext{}
	}
	ctx := smtpCommandContext{username: session.username, accounts: session.deliveries, message: session.completed}
	if ctx.message == nil {
		ctx.message = session.envelope(session.recipients)
	}
	if len(ctx.accounts) == 0 && session.authenticated != nil {
		ctx.accounts = []*smtpDelivery{{account: session.authenticated}}
	}
	return ctx
}
func (b *smtpBackend) hookFaults(c *smtp.Conn, command, phase string, ctx smtpCommandContext) bool {
	e := protocolEvent{protocol: "smtp", command: command, phase: phase, username: ctx.username, message: ctx.message}
	for _, t := range b.service.serverFaults.claim(e, false) {
		if applySMTPFault(c, b.service, t) {
			return true
		}
	}
	for _, delivery := range ctx.accounts {
		if e.message != nil {
			copy := *e.message
			copy.EnvelopeTo = delivery.recipients
			e.message = &copy
		}
		for _, t := range delivery.account.faults.claim(e, false) {
			if applySMTPFault(c, b.service, t) {
				return true
			}
		}
	}
	return false
}
func (b *smtpBackend) installFaultHooks(srv *smtp.Server) {
	srv.ErrorHook = func(c *smtp.Conn, err error) bool {
		var injected *smtpInjectedError
		if !errors.As(err, &injected) {
			return false
		}
		return applySMTPFault(c, b.service, injected.rule)
	}
	srv.CommandHook = func(c *smtp.Conn, command, arg string) bool {
		ctx := smtpContext(c)
		c.HookData = ctx
		if command == "MAIL" || command == "RCPT" {
			return false
		} // parsed envelope/account in backend
		if command == "AUTH" {
			ctx = smtpCommandContext{}
		}
		return b.hookFaults(c, command, "before", ctx)
	}
	srv.ResponseHook = func(c *smtp.Conn, reply *smtp.Reply) *smtp.Reply {
		if reply.Code < 200 || reply.Code >= 400 || reply.Code == 334 || reply.Code == 354 {
			return reply
		}
		command := c.Command()
		ctx := smtpContext(c)
		// DATA resets the session before writing its final result; HookData is set
		// by Data with immutable delivery/message context for that completion.
		if saved, ok := c.HookData.(smtpCommandContext); ok && saved.message != nil && (command == "DATA" || command == "BDAT" || command == "RCPT") {
			ctx = saved
		}
		if command == "RSET" && len(ctx.accounts) == 0 {
			if saved, ok := c.HookData.(smtpCommandContext); ok {
				ctx = saved
			}
		}
		if command == "EHLO" && reply.Code == 250 {
			e := protocolEvent{protocol: "smtp", command: "EHLO", phase: "after", username: ctx.username}
			for _, t := range b.service.serverFaults.claim(e, true) {
				a := t.Action.Parameters.(*capabilitiesAction)
				reply.Lines = append(reply.Lines[:1:1], filterCapabilities(reply.Lines[1:], a.Omit)...)
			}
		}
		if command != "CONNECT" && b.hookFaults(c, command, "after", ctx) {
			return nil
		}
		return reply
	}
}
