package main

import (
	"errors"
	"fmt"
	"net/mail"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	"golang.org/x/text/cases"
	"golang.org/x/text/language"
)

type messagePredicate func(*storedMessage) bool

func searchWords(query string) ([]string, error) {
	var words []string
	var word strings.Builder
	quoted, escape := false, false
	for _, r := range query {
		if escape {
			word.WriteRune(r)
			escape = false
			continue
		}
		if r == '\\' {
			escape = true
			continue
		}
		if r == '"' {
			quoted = !quoted
			continue
		}
		if unicode.IsSpace(r) && !quoted {
			if word.Len() > 0 {
				words = append(words, word.String())
				word.Reset()
			}
			continue
		}
		word.WriteRune(r)
	}
	if quoted || escape {
		return nil, errors.New("unterminated search quote or escape")
	}
	if word.Len() > 0 {
		words = append(words, word.String())
	}
	return words, nil
}
func addressText(list []*mail.Address) string {
	var words []string
	for _, a := range list {
		words = append(words, a.Name, a.Address)
	}
	return strings.Join(words, " ")
}
func headerText(headers map[string][]string, name string) string {
	for key, values := range headers {
		if strings.EqualFold(key, name) {
			return strings.Join(values, ", ")
		}
	}
	return ""
}
func contains(haystack, needle string) bool {
	return strings.Contains(strings.ToLower(haystack), strings.ToLower(needle))
}

func compileSearch(query, tz string) (messagePredicate, error) {
	location := time.UTC
	if tz != "" {
		var err error
		location, err = time.LoadLocation(tz)
		if err != nil {
			return nil, errors.New("invalid timezone")
		}
	}
	words, err := searchWords(query)
	if err != nil {
		return nil, err
	}
	predicates := []messagePredicate{}
	for _, word := range words {
		negate := false
		if strings.HasPrefix(word, "!") || strings.HasPrefix(word, "-") {
			negate = true
			word = word[1:]
		}
		key, value, filter := strings.Cut(word, ":")
		var predicate messagePredicate
		if !filter {
			value = word
			predicate = func(m *storedMessage) bool {
				d := m.Detail
				return contains(d.Subject+" "+d.Text+" "+d.HTML+" "+addressText(append(append(append(append([]*mail.Address{d.From}, d.To...), d.Cc...), d.Bcc...), d.ReplyTo...))+" "+d.MessageID, value)
			}
		} else {
			switch strings.ToLower(key) {
			case "from", "to", "cc", "bcc", "reply-to", "addressed", "subject", "message-id", "tag", "username", "folder", "body":
				predicate = func(m *storedMessage) bool {
					d := m.Detail
					var text string
					switch strings.ToLower(key) {
					case "from":
						text = addressText([]*mail.Address{d.From})
					case "to":
						text = addressText(d.To)
					case "cc":
						text = addressText(d.Cc)
					case "bcc":
						text = addressText(d.Bcc)
					case "reply-to":
						text = addressText(d.ReplyTo)
					case "addressed":
						text = addressText(append(append(append(append([]*mail.Address{d.From}, d.To...), d.Cc...), d.Bcc...), d.ReplyTo...))
					case "subject":
						text = d.Subject
					case "message-id":
						text = d.MessageID
					case "tag":
						for _, tag := range m.Tags {
							if strings.EqualFold(tag, value) {
								return true
							}
						}
						return false
					case "username":
						text = m.Username
					case "folder":
						text = m.Folder
					case "body":
						text = d.Text + " " + d.HTML
					}
					return contains(text, value)
				}
			case "is":
				switch value {
				case "read":
					predicate = func(m *storedMessage) bool { return hasFlag(m.Flags, "\\Seen") }
				case "unread":
					predicate = func(m *storedMessage) bool { return !hasFlag(m.Flags, "\\Seen") }
				case "tagged":
					predicate = func(m *storedMessage) bool { return len(m.Tags) > 0 }
				default:
					return nil, fmt.Errorf("unknown is: filter %q", value)
				}
			case "has":
				switch value {
				case "attachment":
					predicate = func(m *storedMessage) bool { return len(m.Detail.Attachments) > 0 }
				case "inline":
					predicate = func(m *storedMessage) bool { return len(m.Detail.Inline) > 0 }
				default:
					return nil, fmt.Errorf("unknown has: filter %q", value)
				}
			case "larger", "smaller":
				size, err := parseSize(value)
				if err != nil {
					return nil, err
				}
				predicate = func(m *storedMessage) bool {
					if key == "larger" {
						return int64(len(m.Raw)) > size
					}
					return int64(len(m.Raw)) < size
				}
			case "before", "after":
				date, err := parseSearchDate(value, location)
				if err != nil {
					return nil, err
				}
				predicate = func(m *storedMessage) bool {
					if key == "before" {
						return m.Created.Before(date)
					}
					return m.Created.After(date)
				}
			default:
				return nil, fmt.Errorf("unknown search filter %q", key)
			}
		}
		if negate {
			positive := predicate
			predicate = func(m *storedMessage) bool { return !positive(m) }
		}
		predicates = append(predicates, predicate)
	}
	return func(m *storedMessage) bool {
		for _, p := range predicates {
			if !p(m) {
				return false
			}
		}
		return true
	}, nil
}

func parseSize(value string) (int64, error) {
	upper := strings.ToUpper(value)
	factor := float64(1)
	for _, suffix := range []string{"KB", "MB", "K", "M"} {
		if strings.HasSuffix(upper, suffix) {
			if strings.HasPrefix(suffix, "M") {
				factor = 1024 * 1024
			} else {
				factor = 1024
			}
			upper = strings.TrimSuffix(upper, suffix)
			break
		}
	}
	number, err := strconv.ParseFloat(upper, 64)
	if err != nil || number < 0 || number > float64(1<<60)/factor {
		return 0, errors.New("invalid search size")
	}
	return int64(number * factor), nil
}
func parseSearchDate(value string, location *time.Location) (time.Time, error) {
	for _, layout := range []string{time.RFC3339Nano, "2006/01/02 15:04:05", "2006-01-02 15:04:05", "2006/01/02 15:04", "2006-01-02 15:04", "2006/01/02", "2006-01-02", "01/02/2006"} {
		if parsed, err := time.ParseInLocation(layout, value, location); err == nil {
			return parsed, nil
		}
	}
	return time.Time{}, errors.New("invalid search date")
}

var tagName = regexp.MustCompile(`^[a-zA-Z0-9_.@ -]{1,100}$`)

func normalizeTags(tags []string) ([]string, error) {
	out := []string{}
	seen := map[string]bool{}
	for _, tag := range tags {
		tag = strings.TrimSpace(tag)
		if !tagName.MatchString(tag) {
			return nil, fmt.Errorf("invalid tag %q", tag)
		}
		if !seen[tag] {
			out = append(out, tag)
			seen[tag] = true
		}
	}
	return out, nil
}
func (b *mailboxBackend) applyTags(m *storedMessage) error {
	tags, err := normalizeTags(m.Tags)
	if err != nil {
		return err
	}
	c := b.config
	automatic := []string{}
	if !strings.Contains(c.TagsDisable, "x-tags") {
		if header := headerText(m.Headers, "X-Tags"); header != "" {
			automatic = append(automatic, strings.Split(header, ",")...)
		}
	}
	if !strings.Contains(c.TagsDisable, "plus-addresses") {
		for _, list := range [][]*mail.Address{{m.Detail.From}, m.Detail.To, m.Detail.Cc, m.Detail.Bcc} {
			for _, address := range list {
				local, _, _ := strings.Cut(address.Address, "@")
				parts := strings.Split(local, "+")
				automatic = append(automatic, parts[1:]...)
			}
		}
	}
	if c.TagsUsername && m.Username != "" {
		automatic = append(automatic, m.Username)
	}
	for _, tag := range automatic {
		tag = strings.TrimSpace(tag)
		if c.TagsTitleCase {
			tag = cases.Title(language.English).String(tag)
		}
		if tagName.MatchString(tag) {
			tags = append(tags, tag)
		}
	}
	for _, filter := range c.TagFilters {
		predicate, err := compileTagSearch(filter.Match)
		if err != nil {
			return err
		}
		matches := predicate(m)
		if matches {
			tags = append(tags, strings.Split(filter.Tags, ",")...)
		}
	}
	m.Tags, err = normalizeTags(tags)
	return err
}

// Tag expressions allow shell-like quoting, including a quoted search phrase
// inside a single-quoted match. No shell expansion or command execution occurs.
func splitExpressions(input string) ([]string, error) {
	var result []string
	var current strings.Builder
	var quote rune
	escape := false
	for _, ch := range input {
		if escape {
			current.WriteRune(ch)
			escape = false
			continue
		}
		if ch == '\\' {
			escape = true
			continue
		}
		if quote != 0 {
			if ch == quote {
				quote = 0
			} else {
				current.WriteRune(ch)
			}
			continue
		}
		if ch == '"' || ch == '\'' {
			quote = ch
			continue
		}
		if unicode.IsSpace(ch) {
			if current.Len() > 0 {
				result = append(result, current.String())
				current.Reset()
			}
		} else {
			current.WriteRune(ch)
		}
	}
	if quote != 0 || escape {
		return nil, errors.New("unterminated tag expression")
	}
	if current.Len() > 0 {
		result = append(result, current.String())
	}
	return result, nil
}
func compileTagSearch(query string) (messagePredicate, error) {
	words, err := searchWords(query)
	if err != nil {
		return nil, err
	}
	predicates := []messagePredicate{}
	for _, word := range words {
		value := word
		negative := strings.HasPrefix(value, "-") || strings.HasPrefix(value, "!")
		if negative {
			value = value[1:]
		}
		key, _, hasFilter := strings.Cut(value, ":")
		if hasFilter && containsString([]string{"from", "to", "cc", "bcc", "reply-to", "addressed", "subject", "message-id", "username", "folder", "body", "is", "has", "larger", "smaller", "before", "after", "tag"}, key) {
			p, err := compileSearch(strconv.Quote(word), "")
			if err != nil {
				return nil, err
			}
			predicates = append(predicates, p)
		} else {
			predicates = append(predicates, func(m *storedMessage) bool {
				matched := contains(string(m.Raw), value)
				if negative {
					return !matched
				}
				return matched
			})
		}
	}
	return func(m *storedMessage) bool {
		for _, p := range predicates {
			if !p(m) {
				return false
			}
		}
		return true
	}, nil
}
