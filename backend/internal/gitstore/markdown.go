package gitstore

import (
	"bytes"
	"html"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

// Markdown renders a repository's markdown for the panel (D-23, D-73). It
// runs here, in the git container, so a hostile file costs this container's
// CPU and not the backend's. The HTML is safe to put in the panel's page:
//
//   - GitHub's flavour, with raw HTML left out (goldmark's default) and no
//     attribute syntax;
//   - links only to http, https and mailto, or within the repository; any
//     other scheme loses its href;
//   - a link or image within the repository has no URL but data-toft-path,
//     the repository path it names, for the panel to point at its own pages
//     and to load images as data: URLs (relative to dir, the file's own
//     directory; a leading / is the repository's root); a link written with
//     a trailing / also has data-toft-dir;
//   - an image from elsewhere becomes a link to it, since the panel's
//     Content-Security-Policy loads no outside images;
//   - heading ids, and the #anchors that name them, start with
//     user-content-, so a README can't take an id the panel uses.
func Markdown(src []byte, dir string) string {
	pc := parser.NewContext()
	pc.Set(dirKey, dir)
	var b bytes.Buffer
	if err := md.Convert(src, &b, parser.WithContext(pc)); err != nil {
		return "<pre>" + html.EscapeString(string(src)) + "</pre>"
	}
	return b.String()
}

// Refs are what MarkdownRefs links in an issue's or a pull request's text
// (D-81): Keys' issues (STO-12; a project's key and its former ones,
// D-83) as data-toft-issue and, with Pulls, the
// repository's pull requests (!41) as data-toft-pull, for the panel to give
// them its own addresses. Code spans and links are left alone.
type Refs struct {
	Keys  []string
	Pulls bool
	// Mentions are the workspace's handles (D-88): @handle becomes a link
	// without a URL, with data-toft-user.
	Mentions []string
}

// MarkdownRefs is Markdown with refs linked.
func MarkdownRefs(src []byte, refs Refs) string {
	pc := parser.NewContext()
	pc.Set(dirKey, "")
	pc.Set(refsKey, refs)
	var b bytes.Buffer
	if err := md.Convert(src, &b, parser.WithContext(pc)); err != nil {
		return "<pre>" + html.EscapeString(string(src)) + "</pre>"
	}
	return b.String()
}

// Doc is a doc page rendered (D-86): HTML is MarkdownRefs's, with page
// links (page:<id>) as data-toft-page and notes (a quote starting [!NOTE],
// [!TIP], [!IMPORTANT], [!WARNING] or [!CAUTION]) as data-toft-note,
// without the marker. Fences are its blocks, the fenced code whose info
// string starts with toft-, in the order the HTML holds them; Issues,
// Pulls and Pages are what its text links.
type Doc struct {
	HTML   string
	Fences []Fence
	Issues []int
	Pulls  []int
	Pages  []string
}

// Fence is a block of a doc: its info string ("toft-run <id> host") and
// its text.
type Fence struct {
	Info, Body string
}

// MarkdownDoc renders a doc page.
func MarkdownDoc(src []byte, refs Refs) Doc {
	pc := parser.NewContext()
	pc.Set(dirKey, "")
	pc.Set(refsKey, refs)
	d := &Doc{Fences: []Fence{}, Issues: []int{}, Pulls: []int{}, Pages: []string{}}
	pc.Set(docKey, d)
	var b bytes.Buffer
	if err := md.Convert(src, &b, parser.WithContext(pc)); err != nil {
		return Doc{HTML: "<pre>" + html.EscapeString(string(src)) + "</pre>", Fences: []Fence{}, Issues: []int{}, Pulls: []int{}, Pages: []string{}}
	}
	d.HTML = b.String()
	return *d
}

var (
	dirKey   = parser.NewContextKey()
	refsKey  = parser.NewContextKey()
	docKey   = parser.NewContextKey()
	pageLink = regexp.MustCompile(`^page:([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})$`)
	noteKind = regexp.MustCompile(`^\[!(NOTE|TIP|IMPORTANT|WARNING|CAUTION)\]`)
	pullRef  = regexp.MustCompile(`(^|[^\w&!])!(\d{1,9})\b`)
	md       = goldmark.New(
		goldmark.WithExtensions(extension.GFM),
		goldmark.WithParserOptions(
			parser.WithAutoHeadingID(),
			parser.WithASTTransformers(util.Prioritized(linkRewriter{}, 999)),
		),
	)
	scheme = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9+.\-]*:`)
)

type linkRewriter struct{}

func (linkRewriter) Transform(doc *ast.Document, reader text.Reader, pc parser.Context) {
	dir, _ := pc.Get(dirKey).(string)
	page, _ := pc.Get(docKey).(*Doc)
	src := reader.Source()
	var outside []*ast.Image
	var plain []*ast.AutoLink
	var notes []*ast.Blockquote
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		if page != nil {
			switch n := n.(type) {
			case *ast.FencedCodeBlock:
				if n.Info != nil {
					if info := strings.TrimSpace(string(n.Info.Segment.Value(src))); strings.HasPrefix(info, "toft-") {
						var body bytes.Buffer
						for i := 0; i < n.Lines().Len(); i++ {
							seg := n.Lines().At(i)
							body.Write(seg.Value(src))
						}
						page.Fences = append(page.Fences, Fence{Info: info, Body: strings.TrimRight(body.String(), "\n")})
					}
				}
			case *ast.Link:
				if m := pageLink.FindSubmatch(bytes.TrimSpace(n.Destination)); m != nil {
					n.Destination = nil
					n.SetAttributeString("data-toft-page", m[1])
					page.Pages = append(page.Pages, string(m[1]))
					return ast.WalkContinue, nil
				}
			case *ast.Blockquote:
				notes = append(notes, n)
			}
		}
		switch n := n.(type) {
		case *ast.Heading:
			if id, ok := n.AttributeString("id"); ok {
				if b, ok := id.([]byte); ok {
					n.SetAttributeString("id", append([]byte("user-content-"), b...))
				}
			}
		case *ast.Link:
			kind, v := classify(string(n.Destination), dir)
			switch kind {
			case "outside":
				n.SetAttributeString("rel", []byte("nofollow noopener noreferrer"))
				n.SetAttributeString("target", []byte("_blank"))
			case "anchor":
				n.Destination = []byte("#user-content-" + v)
			case "repo":
				if strings.HasSuffix(strings.TrimSpace(strings.SplitN(string(n.Destination), "#", 2)[0]), "/") {
					n.SetAttributeString("data-toft-dir", []byte(""))
				}
				n.Destination = nil
				n.SetAttributeString("data-toft-path", []byte(v))
			default:
				n.Destination = nil
			}
		case *ast.AutoLink:
			// An email's URL has no scheme; the renderer adds mailto:.
			kind := "outside"
			if n.AutoLinkType != ast.AutoLinkEmail {
				kind, _ = classify(string(n.URL(src)), dir)
			}
			if kind == "outside" {
				n.SetAttributeString("rel", []byte("nofollow noopener noreferrer"))
				n.SetAttributeString("target", []byte("_blank"))
			} else {
				plain = append(plain, n)
			}
		case *ast.Image:
			kind, v := classify(string(n.Destination), dir)
			switch kind {
			case "outside":
				outside = append(outside, n)
			case "repo":
				n.Destination = nil
				n.SetAttributeString("data-toft-path", []byte(v))
			default:
				n.Destination = nil
			}
		}
		return ast.WalkContinue, nil
	})
	// Outside images become links to them, their alt text the link's.
	for _, img := range outside {
		link := ast.NewLink()
		link.Destination = img.Destination
		link.SetAttributeString("rel", []byte("nofollow noopener noreferrer"))
		link.SetAttributeString("target", []byte("_blank"))
		for c := img.FirstChild(); c != nil; {
			next := c.NextSibling()
			img.RemoveChild(img, c)
			link.AppendChild(link, c)
			c = next
		}
		if link.ChildCount() == 0 {
			link.AppendChild(link, ast.NewString([]byte("image")))
		}
		img.Parent().ReplaceChild(img.Parent(), img, link)
	}
	// An autolink to another scheme is its text.
	for _, a := range plain {
		a.Parent().ReplaceChild(a.Parent(), a, ast.NewString(a.Label(src)))
	}
	for _, q := range notes {
		markNote(q, src)
	}
	if refs, ok := pc.Get(refsKey).(Refs); ok {
		linkRefs(doc, src, refs, page)
	}
}

// markNote gives a quote that starts with a note's marker data-toft-note,
// and takes the marker out.
func markNote(q *ast.Blockquote, src []byte) {
	p, ok := q.FirstChild().(*ast.Paragraph)
	if !ok || p.Lines().Len() == 0 {
		return
	}
	first := p.Lines().At(0)
	m := noteKind.FindSubmatch(first.Value(src))
	if m == nil {
		return
	}
	q.SetAttributeString("data-toft-note", bytes.ToLower(m[1]))
	// The marker is the paragraph's first texts ("[", "!NOTE", "]").
	left := len(m[0])
	for c := p.FirstChild(); c != nil && left > 0; {
		t, ok := c.(*ast.Text)
		if !ok {
			return
		}
		next := c.NextSibling()
		v := t.Segment.Value(src)
		if len(v) > left {
			t.Segment = t.Segment.WithStart(t.Segment.Start + left)
			return
		}
		left -= len(v)
		p.RemoveChild(p, c)
		c = next
	}
	if p.ChildCount() == 0 {
		q.RemoveChild(q, p)
	}
}

// linkRefs splits the text outside links and code spans around refs,
// each becoming a link without a URL.
func linkRefs(doc *ast.Document, src []byte, refs Refs, page *Doc) {
	var issue *regexp.Regexp
	var quoted []string
	for _, k := range refs.Keys {
		if k != "" {
			quoted = append(quoted, regexp.QuoteMeta(k))
		}
	}
	if len(quoted) > 0 {
		issue = regexp.MustCompile(`\b(?:` + strings.Join(quoted, "|") + `)-0*(\d{1,9})\b`)
	}
	var mention *regexp.Regexp
	var handles []string
	for _, h := range refs.Mentions {
		if h != "" {
			handles = append(handles, regexp.QuoteMeta(h))
		}
	}
	if len(handles) > 0 {
		mention = regexp.MustCompile(`(^|[^\w@.])@(` + strings.Join(handles, "|") + `)\b`)
	}
	if issue == nil && !refs.Pulls && mention == nil {
		return
	}
	var texts []*ast.Text
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch n := n.(type) {
		case *ast.Link, *ast.AutoLink, *ast.CodeSpan, *ast.Image:
			return ast.WalkSkipChildren, nil
		case *ast.Text:
			texts = append(texts, n)
		}
		return ast.WalkContinue, nil
	})
	type found struct {
		start, stop int
		attr, n     string
	}
	for _, t := range texts {
		seg := t.Segment
		v := seg.Value(src)
		var hits []found
		if issue != nil {
			for _, m := range issue.FindAllSubmatchIndex(v, -1) {
				hits = append(hits, found{m[0], m[1], "data-toft-issue", string(v[m[2]:m[3]])})
			}
		}
		if refs.Pulls {
			for _, m := range pullRef.FindAllSubmatchIndex(v, -1) {
				hits = append(hits, found{m[3], m[1], "data-toft-pull", string(v[m[4]:m[5]])})
			}
		}
		if mention != nil {
			for _, m := range mention.FindAllSubmatchIndex(v, -1) {
				hits = append(hits, found{m[3], m[1], "data-toft-user", string(v[m[4]:m[5]])})
			}
		}
		if len(hits) == 0 {
			continue
		}
		sort.Slice(hits, func(i, j int) bool { return hits[i].start < hits[j].start })
		parent, pos := t.Parent(), 0
		piece := func(from, to int) *ast.Text {
			return ast.NewTextSegment(text.NewSegment(seg.Start+from, seg.Start+to))
		}
		for _, r := range hits {
			if r.start < pos {
				continue // overlaps the one before
			}
			if r.start > pos {
				parent.InsertBefore(parent, t, piece(pos, r.start))
			}
			link := ast.NewLink()
			link.SetAttributeString(r.attr, []byte(r.n))
			if page != nil {
				if n, err := strconv.Atoi(r.n); err == nil && r.attr != "data-toft-user" {
					if r.attr == "data-toft-issue" {
						page.Issues = append(page.Issues, n)
					} else {
						page.Pulls = append(page.Pulls, n)
					}
				}
			}
			link.AppendChild(link, piece(r.start, r.stop))
			parent.InsertBefore(parent, t, link)
			pos = r.stop
		}
		rest := piece(pos, len(v))
		rest.SetSoftLineBreak(t.SoftLineBreak())
		rest.SetHardLineBreak(t.HardLineBreak())
		parent.ReplaceChild(parent, t, rest)
	}
}

// classify says what a link's destination is: "outside" (http, https or
// mailto, kept as is), "anchor" (a heading on the page, v its id),
// "repo" (v the repository path it names) or "drop".
func classify(dest, dir string) (kind, v string) {
	// Browsers ignore these anywhere in a URL; so does the check.
	d := strings.Map(func(r rune) rune {
		if r <= 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, dest)
	switch {
	case d == "":
		return "drop", ""
	case strings.HasPrefix(d, "#"):
		return "anchor", d[1:]
	case scheme.MatchString(d):
		s := strings.ToLower(d[:strings.IndexByte(d, ':')])
		if s == "http" || s == "https" || s == "mailto" {
			return "outside", ""
		}
		return "drop", ""
	case strings.HasPrefix(d, "//"), strings.HasPrefix(d, `\`), strings.HasPrefix(d, `/\`):
		// Another host, without a scheme.
		return "drop", ""
	}
	// The path as written (a file name may hold a space), unescaped.
	d = strings.TrimSpace(dest)
	if i := strings.IndexAny(d, "?#"); i >= 0 {
		d = d[:i]
	}
	if u, err := url.PathUnescape(d); err == nil {
		d = u
	}
	var p string
	if strings.HasPrefix(d, "/") {
		p = path.Clean(d[1:])
	} else {
		p = path.Join(dir, d)
	}
	if p == "." {
		p = ""
	}
	if strings.HasPrefix(p, "../") || p == ".." || !ValidPath(p) {
		return "drop", ""
	}
	return "repo", p
}
