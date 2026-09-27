package sandbox

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	"image/png"
	"io"
	"net"
	"net/http"
	"net/mail"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
	"golang.org/x/net/html"
)

// Resolve and dial the same validated address, including on redirects. This
// prevents a link-check request from following DNS changes into private hosts.
func inspectionClient(allowInternal, follow bool) *http.Client {
	transport := &http.Transport{TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: 5 * time.Second}
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		addresses, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil {
			return nil, err
		}
		for _, a := range addresses {
			ip := a.IP
			if !allowInternal && (!ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast()) {
				return nil, errors.New("internal HTTP addresses are disabled")
			}
		}
		var last error
		for _, a := range addresses {
			conn, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, network, net.JoinHostPort(a.IP.String(), port))
			if err == nil {
				return conn, nil
			}
			last = err
		}
		if last == nil {
			last = errors.New("host has no addresses")
		}
		return nil, last
	}
	return &http.Client{Transport: transport, Timeout: 10 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if !follow {
			return http.ErrUseLastResponse
		}
		if len(via) >= 10 {
			return errors.New("too many redirects")
		}
		return nil
	}}
}

func visitHTML(n *html.Node, fn func(*html.Node)) {
	fn(n)
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		visitHTML(c, fn)
	}
}
func attribute(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

func (a *httpAPI) thumbnail(w http.ResponseWriter, r *http.Request) {
	m := a.requireMessage(w, r)
	if m == nil {
		return
	}
	for _, p := range append(m.Detail.Attachments, m.Detail.Inline...) {
		if p.PartID != r.PathValue("part") {
			continue
		}
		config, _, err := image.DecodeConfig(bytes.NewReader(p.Data))
		if err != nil || config.Width <= 0 || config.Height <= 0 || int64(config.Width)*int64(config.Height) > 40_000_000 {
			apiError(w, 400, errors.New("unsupported or oversized image"))
			return
		}
		src, _, err := image.Decode(bytes.NewReader(p.Data))
		if err != nil {
			apiError(w, 400, err)
			return
		}
		width, height := config.Width, config.Height
		if width > 320 {
			height = height * 320 / width
			width = 320
		}
		if height > 320 {
			width = width * 320 / height
			height = 320
		}
		width = max(width, 1)
		height = max(height, 1)
		dst := image.NewRGBA(image.Rect(0, 0, width, height))
		draw.CatmullRom.Scale(dst, dst.Bounds(), src, src.Bounds(), draw.Over, nil)
		w.Header().Set("Content-Type", "image/png")
		png.Encode(w, dst)
		return
	}
	apiError(w, 404, errors.New("MIME part not found"))
}

type linkResult struct {
	URL, Status string
	StatusCode  int
}

var plainURL = regexp.MustCompile(`https?://[^\s<>"']+`)

func (a *httpAPI) linkCheck(w http.ResponseWriter, r *http.Request) {
	m := a.requireMessage(w, r)
	if m == nil {
		return
	}
	follow := false
	if s := r.URL.Query().Get("follow"); s != "" {
		var err error
		follow, err = strconv.ParseBool(s)
		if err != nil {
			apiError(w, 400, err)
			return
		}
	}
	urls := map[string]bool{}
	document, err := html.Parse(strings.NewReader(m.Detail.HTML))
	if err == nil {
		visitHTML(document, func(n *html.Node) {
			for _, attr := range n.Attr {
				if attr.Key == "href" || attr.Key == "src" {
					urls[attr.Val] = true
				}
			}
		})
	}
	for _, u := range plainURL.FindAllString(m.Detail.Text, -1) {
		urls[strings.TrimRight(u, ".,;)")] = true
	}
	keys := []string{}
	for u := range urls {
		if !strings.HasPrefix(u, "#") && !strings.HasPrefix(u, "cid:") && !strings.HasPrefix(u, "data:") {
			keys = append(keys, u)
		}
	}
	sort.Strings(keys)
	if len(keys) > 200 {
		apiError(w, 400, errors.New("link checker supports at most 200 unique links"))
		return
	}
	client := inspectionClient(a.store.config.AllowInternalHTTP, follow)
	defer client.CloseIdleConnections()
	// A small worker pool bounds load and avoids a single slow link serializing all checks.
	results := make([]linkResult, len(keys))
	jobs := make(chan int)
	done := make(chan struct{}, 8)
	for i := 0; i < 8; i++ {
		go func() {
			defer func() { done <- struct{}{} }()
			for i := range jobs {
				u := keys[i]
				result := linkResult{URL: u}
				parsed, e := url.Parse(u)
				if e == nil && parsed.Scheme == "mailto" {
					_, e = mail.ParseAddress(strings.Split(parsed.Opaque, "?")[0])
					if e == nil {
						result.Status = "Valid mailto"
						result.StatusCode = 200
					}
				} else if e == nil && parsed.Scheme == "tel" && parsed.Opaque != "" {
					result.Status = "Telephone link"
					result.StatusCode = 200
				} else if e == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") {
					var req *http.Request
					req, e = http.NewRequestWithContext(r.Context(), http.MethodHead, u, nil)
					if e == nil {
						var resp *http.Response
						resp, e = client.Do(req)
						if e == nil {
							resp.Body.Close()
							if resp.StatusCode == 405 || resp.StatusCode == 501 {
								req.Method = http.MethodGet
								resp, e = client.Do(req)
								if e == nil {
									resp.Body.Close()
								}
							}
							if e == nil {
								result.StatusCode = resp.StatusCode
								result.Status = resp.Status
							}
						}
					}
				} else if e == nil {
					e = errors.New("unsupported or relative URL")
				}
				if e != nil {
					result.Status = e.Error()
				}
				results[i] = result
			}
		}()
	}
	for i := range keys {
		jobs <- i
	}
	close(jobs)
	for i := 0; i < 8; i++ {
		<-done
	}
	errorsCount := 0
	for _, v := range results {
		if v.StatusCode == 0 || v.StatusCode >= 400 {
			errorsCount++
		}
	}
	jsonResponse(w, 200, map[string]any{"Links": results, "Errors": errorsCount})
}

type spamRule struct {
	Description, Name string
	Score             float64
}
type spamResult struct {
	Error  string
	IsSpam bool
	Rules  []spamRule
	Score  float64
}

func checkSpam(ctx context.Context, address string, raw []byte) (spamResult, error) {
	result := spamResult{Rules: []spamRule{}}
	conn, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "tcp", address)
	if err != nil {
		return result, err
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(15 * time.Second))
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	if _, err = fmt.Fprintf(conn, "REPORT SPAMC/1.5\r\nContent-length: %d\r\n\r\n", len(raw)); err != nil {
		return result, err
	}
	if _, err = conn.Write(raw); err != nil {
		return result, err
	}
	scanner := bufio.NewScanner(io.LimitReader(conn, 2<<20))
	scanner.Buffer(make([]byte, 4096), 1<<20)
	if !scanner.Scan() || !strings.HasPrefix(scanner.Text(), "SPAMD/") || !strings.Contains(scanner.Text(), " 0 ") {
		return result, errors.New("SpamAssassin returned a protocol error")
	}
	inBody := false
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			inBody = true
			continue
		}
		if !inBody {
			if strings.HasPrefix(strings.ToLower(line), "spam:") {
				parts := strings.Fields(strings.TrimPrefix(line, "Spam:"))
				if len(parts) >= 3 {
					result.IsSpam = strings.EqualFold(strings.TrimSuffix(parts[0], ";"), "true")
					result.Score, _ = strconv.ParseFloat(parts[2], 64)
				}
			}
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		score, err := strconv.ParseFloat(fields[0], 64)
		if err == nil {
			result.Rules = append(result.Rules, spamRule{Score: score, Name: fields[1], Description: strings.Join(fields[2:], " ")})
		}
	}
	return result, scanner.Err()
}
func (a *httpAPI) spamCheck(w http.ResponseWriter, r *http.Request) {
	m := a.requireMessage(w, r)
	if m == nil {
		return
	}
	if a.store.config.SpamAssassin == "" {
		apiError(w, 400, errors.New("SpamAssassin is not configured"))
		return
	}
	result, err := checkSpam(r.Context(), a.store.config.SpamAssassin, m.Raw)
	if err != nil {
		result.Error = err.Error()
	}
	jsonResponse(w, 200, result)
}
