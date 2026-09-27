package emulator

import (
	_ "embed"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"

	"golang.org/x/net/html"
)

// This is upstream Can I Email data under the MIT license in data/.
// Detection and scoring are implemented here independently of Mailpit.
//
//go:embed data/caniemail.json
var compatibilityJSON []byte

type compatibilityFeature struct {
	Slug, Title, Description, URL, Category, Keywords string
	Tags                                              []string
	Notes                                             map[string]string `json:"notes_by_num"`
	Stats                                             map[string]map[string]map[string]string
}
type compatibilityData struct {
	Updated string `json:"last_update_date"`
	Data    []compatibilityFeature
}

var compatibility = sync.OnceValue(func() compatibilityData {
	var d compatibilityData
	if err := json.Unmarshal(compatibilityJSON, &d); err != nil {
		panic(err)
	}
	return d
})

type compatibilityScore struct {
	Found                           int
	Supported, Unsupported, Partial float64
}
type compatibilityResult struct{ Name, Family, Platform, Version, Support, NoteNumber string }
type compatibilityWarning struct {
	Slug, Title, Category, Description, Keywords, URL string
	Tags                                              []string
	NotesByNumber                                     map[string]string
	Results                                           []compatibilityResult
	Score                                             compatibilityScore
}

var cssDeclaration = regexp.MustCompile(`(?i)([-a-z]+)\s*:\s*([^;{}]+)`)
var cssWord = regexp.MustCompile(`[-a-z]+`)
var htmlElementTitle = regexp.MustCompile(`<([a-z][a-z0-9-]*)\b`)
var cssComment = regexp.MustCompile(`(?s)/\*.*?\*/`)

func countFeature(f compatibilityFeature, nodes []*html.Node, css string, declarations map[string][]string) int {
	slug := strings.TrimPrefix(f.Slug, "css-")
	if f.Category == "html" {
		tags := htmlElementTitle.FindAllStringSubmatch(strings.ToLower(f.Title), -1)
		count := 0
		for _, n := range nodes {
			matched := false
			for _, t := range tags {
				if n.Data == t[1] {
					matched = true
				}
			}
			if len(tags) == 0 {
				key := strings.TrimSuffix(strings.TrimPrefix(f.Slug, "html-"), "-attribute")
				for _, at := range n.Attr {
					if at.Key == key {
						matched = true
					}
				}
			}
			if matched {
				count++
			}
		}
		return count
	}
	if f.Category == "image" {
		count := 0
		ext := strings.TrimPrefix(f.Slug, "image-")
		for _, n := range nodes {
			src := strings.ToLower(attribute(n, "src"))
			if n.Data == "img" && (strings.Contains(src, "."+ext) || strings.HasPrefix(src, "data:image/"+ext)) {
				count++
			}
		}
		return count
	}
	if f.Category != "css" {
		if f.Slug == "amp" {
			for _, n := range nodes {
				for _, at := range n.Attr {
					if at.Key == "amp4email" || at.Key == "⚡4email" {
						return 1
					}
				}
			}
		}
		return 0
	}
	if slug == "comments" {
		return len(cssComment.FindAllString(css, -1))
	}
	if strings.HasPrefix(slug, "at-") {
		name := strings.Split(strings.TrimPrefix(slug, "at-"), "-")[0]
		if name == "font" {
			name = "font-face"
		}
		count := strings.Count(css, "@"+name)
		if name == "media" && slug != "at-media" {
			term := strings.TrimPrefix(slug, "at-media-")
			return min(count, strings.Count(css, term))
		}
		return count
	}
	if strings.HasPrefix(slug, "display-") {
		count := 0
		for _, v := range declarations["display"] {
			if strings.EqualFold(strings.TrimSpace(v), strings.TrimPrefix(slug, "display-")) {
				count++
			}
		}
		return count
	}
	if strings.Contains(f.Title, "()") {
		name := strings.TrimSuffix(strings.TrimPrefix(slug, "function-"), "-function")
		return strings.Count(css, name+"(")
	}
	if strings.HasPrefix(slug, "unit-") {
		return len(regexp.MustCompile(`[0-9.]`+regexp.QuoteMeta(strings.TrimPrefix(slug, "unit-"))+`\b`).FindAllString(css, -1))
	}
	if strings.HasPrefix(slug, "pseudo-") {
		token := strings.TrimPrefix(slug, "pseudo-")
		token = strings.TrimPrefix(token, "class-")
		token = strings.TrimPrefix(token, "element-")
		return strings.Count(css, ":"+token)
	}
	if strings.HasPrefix(slug, "selector-") {
		tokens := map[string]string{"selector-child": ">", "selector-adjacent-sibling": "+", "selector-general-sibling": "~", "selector-universal": "*", "selector-attribute": "[", "selector-class": ".", "selector-id": "#"}
		if token := tokens[slug]; token != "" {
			count := 0
			for _, piece := range strings.Split(css, "}") {
				selector, _, _ := strings.Cut(piece, "{")
				if strings.Contains(selector, token) {
					count++
				}
			}
			return count
		}
	}
	// Exact property detection covers simple properties; feature titles list the
	// alternatives for grouped logical properties (e.g. block-size & inline-size).
	keys := []string{slug}
	for _, word := range cssWord.FindAllString(strings.ToLower(f.Title), -1) {
		if strings.Contains(word, "-") {
			keys = append(keys, word)
		}
	}
	seen := map[string]bool{}
	count := 0
	for _, key := range keys {
		if !seen[key] {
			seen[key] = true
			count += len(declarations[key])
		}
	}
	return count
}

func (a *httpAPI) htmlCheck(w http.ResponseWriter, r *http.Request) {
	m := a.requireMessage(w, r)
	if m == nil {
		return
	}
	root, err := html.Parse(strings.NewReader(m.Detail.HTML))
	if err != nil {
		apiError(w, 400, err)
		return
	}
	nodes := []*html.Node{}
	var css strings.Builder
	remote := []string{}
	visitHTML(root, func(n *html.Node) {
		if n.Type != html.ElementNode {
			return
		}
		nodes = append(nodes, n)
		if style := attribute(n, "style"); style != "" {
			css.WriteString("x{" + style + "}\n")
		}
		if n.Data == "style" {
			for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
				if ch.Type == html.TextNode {
					css.WriteString(ch.Data)
				}
			}
		}
		if n.Data == "link" && strings.EqualFold(attribute(n, "rel"), "stylesheet") {
			remote = append(remote, attribute(n, "href"))
		}
	})
	remoteErrors := []string{}
	if !a.store.config.BlockRemoteCSS {
		client := inspectionClient(a.store.config.AllowInternalHTTP, true)
		defer client.CloseIdleConnections()
		for i, link := range remote {
			if i >= 10 {
				remoteErrors = append(remoteErrors, "remote stylesheet limit reached")
				break
			}
			u, e := url.Parse(link)
			if e != nil || (u.Scheme != "http" && u.Scheme != "https") {
				remoteErrors = append(remoteErrors, "invalid stylesheet URL")
				continue
			}
			req, e := http.NewRequestWithContext(r.Context(), "GET", link, nil)
			if e != nil {
				continue
			}
			resp, e := client.Do(req)
			if e != nil {
				remoteErrors = append(remoteErrors, e.Error())
				continue
			}
			data, e := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
			resp.Body.Close()
			if e != nil || resp.StatusCode != 200 {
				remoteErrors = append(remoteErrors, "stylesheet could not be loaded")
				continue
			}
			css.Write(data)
			css.WriteByte('\n')
		}
	}
	cssText := strings.ToLower(css.String())
	declarations := map[string][]string{}
	for _, d := range cssDeclaration.FindAllStringSubmatch(cssComment.ReplaceAllString(cssText, ""), -1) {
		declarations[d[1]] = append(declarations[d[1]], strings.TrimSpace(d[2]))
	}
	data := compatibility()
	warnings := []compatibilityWarning{}
	platforms := map[string][]string{}
	tests := 0
	supported, unsupported, partial := 0.0, 0.0, 0.0
	for _, f := range data.Data {
		found := countFeature(f, nodes, cssText, declarations)
		if found == 0 {
			continue
		}
		warning := compatibilityWarning{Slug: f.Slug, Title: f.Title, Category: f.Category, Description: f.Description, Keywords: f.Keywords, URL: f.URL, Tags: f.Tags, NotesByNumber: f.Notes, Results: []compatibilityResult{}, Score: compatibilityScore{Found: found}}
		total, yes, no, part := 0.0, 0.0, 0.0, 0.0
		for family, ps := range f.Stats {
			for platform, versions := range ps {
				if !containsString(platforms[family], platform) {
					platforms[family] = append(platforms[family], platform)
				}
				for version, raw := range versions {
					fields := strings.Fields(raw)
					if len(fields) == 0 {
						continue
					}
					support := "partial"
					switch fields[0] {
					case "y":
						support = "yes"
						yes++
					case "n":
						support = "no"
						no++
					default:
						part++
					}
					total++
					note := ""
					if len(fields) > 1 {
						note = strings.TrimPrefix(fields[1], "#")
					}
					warning.Results = append(warning.Results, compatibilityResult{Name: family + " " + platform + " " + version, Family: family, Platform: platform, Version: version, Support: support, NoteNumber: note})
				}
			}
		}
		sort.Slice(warning.Results, func(i, j int) bool { return warning.Results[i].Name < warning.Results[j].Name })
		if total > 0 {
			warning.Score.Supported = yes / total * 100
			warning.Score.Unsupported = no / total * 100
			warning.Score.Partial = part / total * 100
		}
		tests += found
		supported += warning.Score.Supported * float64(found)
		unsupported += warning.Score.Unsupported * float64(found)
		partial += warning.Score.Partial * float64(found)
		if no+part > 0 {
			warnings = append(warnings, warning)
		}
	}
	for family := range platforms {
		sort.Strings(platforms[family])
	}
	if tests > 0 {
		supported /= float64(tests)
		unsupported /= float64(tests)
		partial /= float64(tests)
	}
	jsonResponse(w, 200, map[string]any{"Platforms": platforms, "Warnings": warnings, "Total": map[string]any{"Nodes": len(nodes), "Tests": tests, "Supported": supported, "Unsupported": unsupported, "Partial": partial}, "DataUpdated": data.Updated, "RemoteCSSErrors": remoteErrors, "Scoring": "Equal weight per recorded client/version; heuristic feature detection, not rendering validation"})
}
func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
