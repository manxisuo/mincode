package webfetch

import (
	"context"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"syscall"
	"time"
)

const (
	defaultMaxBytes = 5 << 20 // 5 MiB
	defaultTimeout  = 20 * time.Second
)

// HTTPFetcher retrieves URLs over HTTP(S) with SSRF protection.
type HTTPFetcher struct {
	Client *http.Client
	// allowPrivate is for tests only: when true the SSRF guard is disabled so
	// tests can reach httptest servers on loopback.
	allowPrivate bool
}

// NewHTTPFetcher builds an SSRF-safe HTTP fetcher.
func NewHTTPFetcher(timeout time.Duration) *HTTPFetcher {
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	f := &HTTPFetcher{}

	tr := http.DefaultTransport.(*http.Transport).Clone()
	// No proxy: a proxy would bypass the per-connection SSRF check below.
	tr.Proxy = nil
	dialer := &net.Dialer{
		Timeout: timeout,
		Control: func(_, address string, _ syscall.RawConn) error {
			return f.checkAddress(address)
		},
	}
	tr.DialContext = dialer.DialContext

	f.Client = &http.Client{
		Timeout:   timeout,
		Transport: tr,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return fmt.Errorf("too many redirects")
			}
			return nil
		},
	}
	return f
}

func (f *HTTPFetcher) Name() string { return "http" }

// checkAddress rejects private/loopback/link-local targets at dial time, which
// also defends against DNS rebinding (the actual connected IP is inspected).
func (f *HTTPFetcher) checkAddress(address string) error {
	if f.allowPrivate {
		return nil
	}
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return fmt.Errorf("%w: %s", ErrBlockedHost, host)
	}
	if isDisallowedIP(ip) {
		return fmt.Errorf("%w: %s", ErrBlockedHost, ip)
	}
	return nil
}

// isDisallowedIP reports whether ip must not be fetched.
func isDisallowedIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() {
		return true
	}
	if v4 := ip.To4(); v4 != nil {
		// 0.0.0.0/8
		if v4[0] == 0 {
			return true
		}
		// 100.64.0.0/10 (CGNAT)
		if v4[0] == 100 && v4[1]&0xc0 == 64 {
			return true
		}
	}
	return false
}

// Fetch implements Fetcher.
func (f *HTTPFetcher) Fetch(ctx context.Context, req Request) (*Response, error) {
	raw := strings.TrimSpace(req.URL)
	if raw == "" {
		return nil, ErrEmptyURL
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, &FetchError{URL: raw, Err: err}
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, ErrBadScheme
	}
	if u.Host == "" {
		return nil, &FetchError{URL: raw, Err: fmt.Errorf("missing host")}
	}

	limit := req.MaxBytes
	if limit <= 0 {
		limit = defaultMaxBytes
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, &FetchError{URL: raw, Err: err}
	}
	httpReq.Header.Set("User-Agent", "Mozilla/5.0 (compatible; mincode-webfetch/1.0)")
	httpReq.Header.Set("Accept", "text/html,application/xhtml+xml,text/plain,application/json;q=0.9,*/*;q=0.1")

	resp, err := f.Client.Do(httpReq)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, &FetchError{URL: raw, Err: err}
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &FetchError{URL: raw, Status: resp.StatusCode}
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, int64(limit)+1))
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, &FetchError{URL: raw, Status: resp.StatusCode, Err: err}
	}
	truncated := len(body) > limit
	if truncated {
		body = body[:limit]
	}

	ct := resp.Header.Get("Content-Type")
	out := &Response{
		URL:         resp.Request.URL.String(),
		Status:      resp.StatusCode,
		ContentType: ct,
		Bytes:       len(body),
		Truncated:   truncated,
	}

	switch {
	case isHTML(ct):
		out.Title, out.Text = htmlToText(string(body))
	case isTextual(ct):
		out.Text = string(body)
	default:
		if isBinary(body) {
			return nil, ErrNotText
		}
		out.Text = string(body)
	}
	out.Text = strings.TrimSpace(out.Text)
	return out, nil
}

func isHTML(ct string) bool {
	ct = strings.ToLower(ct)
	return strings.Contains(ct, "html") || strings.Contains(ct, "xhtml")
}

func isTextual(ct string) bool {
	ct = strings.ToLower(ct)
	return ct == "" || strings.HasPrefix(ct, "text/") ||
		strings.Contains(ct, "json") || strings.Contains(ct, "xml")
}

func isBinary(b []byte) bool {
	n := len(b)
	if n > 8000 {
		n = 8000
	}
	for i := 0; i < n; i++ {
		if b[i] == 0 {
			return true
		}
	}
	return false
}

var (
	reComment  = regexp.MustCompile(`(?is)<!--.*?-->`)
	reScript   = regexp.MustCompile(`(?is)<script\b.*?</script>`)
	reStyle    = regexp.MustCompile(`(?is)<style\b.*?</style>`)
	reNoscript = regexp.MustCompile(`(?is)<noscript\b.*?</noscript>`)
	reBlock    = regexp.MustCompile(`(?i)</?(p|div|br|h[1-6]|li|ul|ol|tr|td|th|table|section|article|header|footer|nav|blockquote|pre|hr)[^>]*>`)
	reTag      = regexp.MustCompile(`(?s)<[^>]*>`)
	reTitle    = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)
)

// htmlToText extracts a page title and readable text from HTML.
func htmlToText(page string) (title, text string) {
	if m := reTitle.FindStringSubmatch(page); len(m) == 2 {
		title = strings.TrimSpace(html.UnescapeString(reTag.ReplaceAllString(m[1], "")))
	}
	body := reScript.ReplaceAllString(page, " ")
	body = reStyle.ReplaceAllString(body, " ")
	body = reNoscript.ReplaceAllString(body, " ")
	body = reComment.ReplaceAllString(body, " ")
	body = reBlock.ReplaceAllString(body, "\n")
	body = reTag.ReplaceAllString(body, " ")
	body = html.UnescapeString(body)

	lines := strings.Split(body, "\n")
	out := make([]string, 0, len(lines))
	for _, ln := range lines {
		ln = strings.Join(strings.Fields(ln), " ")
		if ln != "" {
			out = append(out, ln)
		}
	}
	return title, strings.Join(out, "\n")
}
