package sandbox

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"
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
func addressText(list []*address) string {
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
				return contains(d.Subject+" "+d.Text+" "+d.HTML+" "+addressText(append(append(append(append([]*address{d.From}, d.To...), d.Cc...), d.Bcc...), d.ReplyTo...))+" "+d.MessageID, value)
			}
		} else {
			switch strings.ToLower(key) {
			case "from", "to", "cc", "bcc", "reply-to", "addressed", "subject", "message-id", "username", "folder", "body":
				predicate = func(m *storedMessage) bool {
					d := m.Detail
					var text string
					switch strings.ToLower(key) {
					case "from":
						text = addressText([]*address{d.From})
					case "to":
						text = addressText(d.To)
					case "cc":
						text = addressText(d.Cc)
					case "bcc":
						text = addressText(d.Bcc)
					case "reply-to":
						text = addressText(d.ReplyTo)
					case "addressed":
						text = addressText(append(append(append(append([]*address{d.From}, d.To...), d.Cc...), d.Bcc...), d.ReplyTo...))
					case "subject":
						text = d.Subject
					case "message-id":
						text = d.MessageID
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
