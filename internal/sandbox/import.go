package sandbox

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/mail"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/johlo/go-imap/v2"
)

// Imports load mailbox exports in one batch. Problems with single messages are
// reported and skipped; problems with the request store nothing.

const maxImportProblems = 20

type importReport struct {
	Imported   int             `json:"imported"`
	Duplicates int             `json:"duplicates"`
	Skipped    int             `json:"skipped"`
	Folders    map[string]int  `json:"folders"`
	Problems   []importProblem `json:"problems"`
}
type importProblem struct {
	Source string `json:"source"`
	Error  string `json:"error"`
}

func (r *importReport) skip(source string, err error) {
	r.Skipped++
	if len(r.Problems) < maxImportProblems {
		r.Problems = append(r.Problems, importProblem{source, err.Error()})
	}
}

type importer struct {
	maxSize int64
	report  importReport
	items   []batchItem
}

// add parses one message and queues it for each folder.
func (im *importer) add(source string, raw []byte, folders []string, date time.Time, flags []string) {
	if int64(len(raw)) > im.maxSize {
		im.report.skip(source, errors.New("message exceeds size limit"))
		return
	}
	fallback := date
	if fallback.IsZero() {
		fallback = time.Now().UTC()
	}
	d, h, err := parseMessage(raw, fallback, "", nil)
	if err != nil {
		im.report.skip(source, err)
		return
	}
	if date.IsZero() {
		date = d.Date
	}
	for i, folder := range folders {
		if strings.EqualFold(folder, "INBOX") {
			folders[i] = "INBOX"
		}
		if !validFolder(folder) {
			im.report.skip(source, fmt.Errorf("invalid folder name %q", folder))
			return
		}
	}
	for _, folder := range folders {
		im.items = append(im.items, batchItem{Raw: raw, Folder: folder, Date: date, Flags: flags, Parsed: parsedMessage{detail: d, headers: h}})
	}
}

func (a *accountAPI) importMessages(w http.ResponseWriter, r *http.Request) {
	media, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	query := r.URL.Query()
	folder, labels := query.Get("folder"), query.Get("labels")
	if labels != "" && labels != "gmail" {
		apiError(w, 400, errors.New("labels must be gmail"))
		return
	}
	// Exports can take longer to upload than the server's request timeout.
	_ = http.NewResponseController(w).SetReadDeadline(time.Time{})
	body, err := decompress(bufio.NewReader(r.Body))
	if err != nil {
		apiError(w, 400, err)
		return
	}
	im := &importer{maxSize: a.account.options.MaxSize}
	switch media {
	case "application/mbox":
		if folder == "" {
			folder = "INBOX"
		}
		err = im.readMbox(body, folder, labels == "gmail")
	case "application/x-tar":
		if folder != "" || labels != "" {
			apiError(w, 400, errors.New("folder and labels apply only to mbox imports"))
			return
		}
		err = im.readMaildir(tar.NewReader(body))
	default:
		apiError(w, 415, errors.New("Content-Type must be application/mbox or application/x-tar"))
		return
	}
	if err != nil {
		apiError(w, 400, err)
		return
	}
	stored, duplicates, err := a.account.importBatch(im.items)
	if err != nil {
		status := 400
		if errors.Is(err, errQuota) {
			status = 507
		}
		apiError(w, status, err)
		return
	}
	report := im.report
	report.Imported, report.Duplicates, report.Folders = len(stored), duplicates, map[string]int{}
	for _, m := range stored {
		report.Folders[m.Folder]++
	}
	if report.Problems == nil {
		report.Problems = []importProblem{}
	}
	jsonResponse(w, 200, report)
}

// decompress transparently accepts gzip, which exports are often shipped in.
func decompress(r *bufio.Reader) (io.Reader, error) {
	magic, _ := r.Peek(2)
	if bytes.Equal(magic, []byte{0x1f, 0x8b}) {
		return gzip.NewReader(r)
	}
	return r, nil
}

// readMbox splits an mbox file on "From " lines that start the file, follow a
// blank line or carry a separator date, and undoes mboxrd ">From " quoting.
// Line endings become CRLF.
func (im *importer) readMbox(r io.Reader, folder string, gmail bool) error {
	lines := bufio.NewReaderSize(r, 64<<10)
	var raw bytes.Buffer
	var separator string
	count, oversized, blank := 0, false, true
	flush := func() {
		if count == 0 {
			return
		}
		source := "message " + strconv.Itoa(count)
		if oversized {
			im.report.skip(source, errors.New("message exceeds size limit"))
			return
		}
		// A blank line before the next separator belongs to the mbox format.
		message := raw.Bytes()
		if blank {
			message = bytes.TrimSuffix(message, []byte("\r\n"))
		}
		im.addMbox(source, append([]byte(nil), message...), separator, folder, gmail)
	}
	for {
		line, err := lines.ReadSlice('\n')
		if err == bufio.ErrBufferFull {
			// ReadSlice's buffer is reused, so copy before reading the rest.
			first := append([]byte(nil), line...)
			var rest []byte
			rest, err = lines.ReadBytes('\n')
			line = append(first, rest...)
		}
		if err != nil && err != io.EOF {
			return fmt.Errorf("read mbox: %w", err)
		}
		if len(line) == 0 && err == io.EOF {
			break
		}
		text := bytes.TrimRight(line, "\r\n")
		if bytes.HasPrefix(text, []byte("From ")) && (blank || !separatorDate(string(text)).IsZero()) {
			flush()
			count++
			raw.Reset()
			separator, oversized, blank = string(text), false, false
		} else {
			if count == 0 {
				if len(bytes.TrimSpace(text)) == 0 {
					continue
				}
				return errors.New("not an mbox file: it must start with a \"From \" line")
			}
			blank = len(text) == 0
			if quoted := bytes.TrimLeft(text, ">"); len(quoted) < len(text) && bytes.HasPrefix(quoted, []byte("From ")) {
				text = text[1:]
			}
			if !oversized {
				raw.Write(text)
				raw.WriteString("\r\n")
				oversized = int64(raw.Len()) > im.maxSize+2
			}
		}
		if err == io.EOF {
			break
		}
	}
	flush()
	return nil
}

func (im *importer) addMbox(source string, raw []byte, separator, folder string, gmail bool) {
	header, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		im.report.skip(source, fmt.Errorf("invalid MIME message: %w", err))
		return
	}
	h := header.Header
	date := separatorDate(separator)
	folders := []string{folder}
	var flags []string
	switch {
	case gmail:
		var skip error
		folders, flags, skip = gmailLabels(h.Get("X-Gmail-Labels"))
		if skip != nil {
			im.report.skip(source, skip)
			return
		}
	case h.Get("X-Mozilla-Status") != "":
		status, _ := strconv.ParseUint(h.Get("X-Mozilla-Status"), 16, 16)
		if status&0x0008 != 0 {
			im.report.skip(source, errors.New("deleted in the source mailbox"))
			return
		}
		for bit, flag := range map[uint64]imap.Flag{0x0001: imap.FlagSeen, 0x0002: imap.FlagAnswered, 0x0004: imap.FlagFlagged} {
			if status&bit != 0 {
				flags = append(flags, string(flag))
			}
		}
	default:
		for _, c := range h.Get("Status") + h.Get("X-Status") {
			if flag, ok := map[rune]imap.Flag{'R': imap.FlagSeen, 'A': imap.FlagAnswered, 'F': imap.FlagFlagged, 'D': imap.FlagDeleted, 'T': imap.FlagDraft}[c]; ok {
				flags = append(flags, string(flag))
			}
		}
	}
	sort.Strings(flags)
	im.add(source, stripHeaders(raw, mboxHeaders), folders, date, flags)
}

// Headers that mail programs add to mbox exports; a server never sends them.
var mboxHeaders = map[string]bool{"Status": true, "X-Status": true, "X-Mozilla-Status": true, "X-Mozilla-Status2": true, "X-Mozilla-Keys": true, "X-Gmail-Labels": true, "X-Gm-Thrid": true}

// stripHeaders removes the named header fields, including folded lines.
func stripHeaders(raw []byte, names map[string]bool) []byte {
	end := len(raw)
	if index := bytes.Index(raw, []byte("\r\n\r\n")); index >= 0 {
		end = index + 2
	}
	var out bytes.Buffer
	dropping := false
	for _, line := range bytes.SplitAfter(raw[:end], []byte("\r\n")) {
		if len(line) > 0 && line[0] != ' ' && line[0] != '\t' {
			name, _, _ := bytes.Cut(line, []byte(":"))
			dropping = names[http.CanonicalHeaderKey(string(bytes.TrimSpace(name)))]
		}
		if !dropping {
			out.Write(line)
		}
	}
	out.Write(raw[end:])
	return out.Bytes()
}

// separatorDate reads the delivery time from "From sender date". Gmail writes
// a numeric zone; other programs write asctime.
func separatorDate(line string) time.Time {
	fields := strings.Fields(line)
	if len(fields) < 3 {
		return time.Time{}
	}
	value := strings.Join(fields[2:], " ")
	for _, layout := range []string{time.ANSIC, "Mon Jan _2 15:04:05 -0700 2006", time.UnixDate, "Mon Jan _2 15:04 2006"} {
		if t, err := time.Parse(layout, value); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}

var gmailFolders = map[string]string{"inbox": "INBOX", "sent": "Sent", "drafts": "Drafts", "spam": "Spam", "trash": "Trash"}
var gmailStates = map[string]bool{"important": true, "opened": true, "unread": true, "starred": true, "archived": true}

// gmailLabels maps Google Takeout labels to folders and flags. A message gets
// a copy in each labelled folder, as Gmail shows it over IMAP; messages with
// no folder label were archived.
func gmailLabels(header string) (folders, flags []string, skip error) {
	decoded, err := new(mime.WordDecoder).DecodeHeader(header)
	if err != nil {
		decoded = header
	}
	labels := []string{}
	if strings.TrimSpace(decoded) != "" {
		reader := csv.NewReader(strings.NewReader(decoded))
		reader.LazyQuotes = true
		if labels, err = reader.Read(); err != nil {
			labels = strings.Split(decoded, ",")
		}
	}
	unread, seen := false, map[string]bool{}
	for _, label := range labels {
		label = strings.TrimSpace(label)
		lower := strings.ToLower(label)
		switch {
		case label == "":
		case lower == "chat":
			return nil, nil, errors.New("Gmail chat transcript, not an email")
		case lower == "unread":
			unread = true
		case lower == "starred":
			flags = append(flags, string(imap.FlagFlagged))
		case gmailStates[lower] || strings.HasPrefix(lower, "category "):
		default:
			folder := label
			if mapped, ok := gmailFolders[lower]; ok {
				folder = mapped
			}
			if lower == "drafts" {
				flags = append(flags, string(imap.FlagDraft))
			}
			if !seen[folder] {
				seen[folder] = true
				folders = append(folders, folder)
			}
		}
	}
	if !unread {
		flags = append(flags, string(imap.FlagSeen))
	}
	if len(folders) == 0 {
		folders = []string{"Archive"}
	}
	return folders, flags, nil
}

// readMaildir imports a tar of a Maildir root. Its cur and new directories
// are INBOX, Maildir++ ".Work.Projects" is Work/Projects, and plain nested
// directories (Work/Projects/cur) keep their path.
func (im *importer) readMaildir(r *tar.Reader) error {
	type entry struct {
		source, folder string
		date           time.Time
		flags          []string
		raw            []byte
	}
	var entries []entry
	for {
		header, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("read tar: %w", err)
		}
		name := path.Clean(strings.TrimPrefix(header.Name, "./"))
		dir, file := path.Split(name)
		parent, kind := path.Split(strings.TrimSuffix(dir, "/"))
		if header.Typeflag != tar.TypeReg || (kind != "cur" && kind != "new") || strings.HasPrefix(file, ".") {
			continue
		}
		if header.Size > im.maxSize {
			im.report.skip(name, errors.New("message exceeds size limit"))
			continue
		}
		raw, err := io.ReadAll(r)
		if err != nil {
			return fmt.Errorf("read tar: %w", err)
		}
		e := entry{source: name, folder: maildirFolder(strings.TrimSuffix(parent, "/")), date: header.ModTime.UTC(), raw: bytes.ReplaceAll(bytes.ReplaceAll(raw, []byte("\r\n"), []byte("\n")), []byte("\n"), []byte("\r\n"))}
		if kind == "cur" {
			e.flags = maildirFlags(file)
		}
		entries = append(entries, e)
	}
	// Archive order is arbitrary; deliver oldest first so UIDs follow time.
	sort.SliceStable(entries, func(i, j int) bool {
		if !entries[i].date.Equal(entries[j].date) {
			return entries[i].date.Before(entries[j].date)
		}
		return entries[i].source < entries[j].source
	})
	for _, e := range entries {
		im.add(e.source, e.raw, []string{e.folder}, e.date, e.flags)
	}
	return nil
}

func maildirFolder(dir string) string {
	switch {
	case dir == "" || dir == ".":
		return "INBOX"
	case !strings.Contains(dir, "/") && strings.HasPrefix(dir, "."):
		return strings.ReplaceAll(strings.TrimPrefix(dir, "."), ".", "/")
	case strings.EqualFold(dir, "INBOX"):
		return "INBOX"
	}
	return dir
}

// maildirFlags reads the ":2,FLAGS" suffix; Dovecot may use ";" instead.
func maildirFlags(file string) []string {
	index := strings.LastIndex(file, ":2,")
	if index < 0 {
		index = strings.LastIndex(file, ";2,")
	}
	if index < 0 {
		return nil
	}
	var flags []string
	for _, c := range file[index+3:] {
		if flag, ok := map[rune]imap.Flag{'S': imap.FlagSeen, 'R': imap.FlagAnswered, 'F': imap.FlagFlagged, 'T': imap.FlagDeleted, 'D': imap.FlagDraft}[c]; ok {
			flags = append(flags, string(flag))
		}
	}
	sort.Strings(flags)
	return flags
}
