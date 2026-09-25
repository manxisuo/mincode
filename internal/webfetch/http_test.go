package webfetch

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func testFetcher() *HTTPFetcher {
	f := NewHTTPFetcher(0)
	f.allowPrivate = true // reach httptest loopback
	return f
}

func TestHTTPFetcherHTMLToText(t *testing.T) {
	const page = `<html><head><title>Hello &amp; World</title><style>body{color:red}</style></head>` +
		`<body><h1>Header</h1><p>Some <b>bold</b> text.</p><script>alert(1)</script><div>More</div></body></html>`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(page))
	}))
	defer srv.Close()

	resp, err := testFetcher().Fetch(context.Background(), Request{URL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Title != "Hello & World" {
		t.Fatalf("title = %q", resp.Title)
	}
	for _, want := range []string{"Header", "Some bold text.", "More"} {
		if !strings.Contains(resp.Text, want) {
			t.Fatalf("missing %q in:\n%s", want, resp.Text)
		}
	}
	for _, bad := range []string{"alert", "color:red"} {
		if strings.Contains(resp.Text, bad) {
			t.Fatalf("script/style leaked: %q in:\n%s", bad, resp.Text)
		}
	}
	if resp.Status != 200 {
		t.Fatalf("status = %d", resp.Status)
	}
}

func TestHTTPFetcherPlainText(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("  hello world  \n"))
	}))
	defer srv.Close()

	resp, err := testFetcher().Fetch(context.Background(), Request{URL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != "hello world" {
		t.Fatalf("text = %q", resp.Text)
	}
}

func TestHTTPFetcherHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	_, err := testFetcher().Fetch(context.Background(), Request{URL: srv.URL})
	var fe *FetchError
	if !errors.As(err, &fe) {
		t.Fatalf("err = %v, want FetchError", err)
	}
	if fe.Status != http.StatusNotFound {
		t.Fatalf("status = %d", fe.Status)
	}
}

func TestHTTPFetcherBadScheme(t *testing.T) {
	for _, u := range []string{"ftp://example.com", "file:///etc/passwd", "javascript:alert(1)"} {
		if _, err := testFetcher().Fetch(context.Background(), Request{URL: u}); !errors.Is(err, ErrBadScheme) {
			t.Fatalf("%q => %v, want ErrBadScheme", u, err)
		}
	}
}

func TestHTTPFetcherEmptyURL(t *testing.T) {
	if _, err := testFetcher().Fetch(context.Background(), Request{URL: "  "}); !errors.Is(err, ErrEmptyURL) {
		t.Fatalf("err = %v, want ErrEmptyURL", err)
	}
}

func TestHTTPFetcherBlocksPrivateTargets(t *testing.T) {
	f := NewHTTPFetcher(0) // allowPrivate=false (production behavior)
	for _, u := range []string{
		"http://127.0.0.1:9/",
		"http://localhost:9/",
		"http://10.0.0.1/",
		"http://192.168.1.1/",
		"http://169.254.169.254/latest/meta-data/",
		"http://[::1]/",
	} {
		_, err := f.Fetch(context.Background(), Request{URL: u})
		if !errors.Is(err, ErrBlockedHost) {
			t.Fatalf("%q => %v, want ErrBlockedHost", u, err)
		}
	}
}

func TestIsDisallowedIP(t *testing.T) {
	blocked := []string{
		"127.0.0.1", "10.0.0.1", "192.168.1.1", "172.16.0.1", "169.254.169.254",
		"0.0.0.0", "100.64.0.1", "224.0.0.1", "::1", "fe80::1",
	}
	for _, s := range blocked {
		if !isDisallowedIP(net.ParseIP(s)) {
			t.Fatalf("%s should be blocked", s)
		}
	}
	allowed := []string{"8.8.8.8", "1.1.1.1", "93.184.216.34", "2606:4700:4700::1111"}
	for _, s := range allowed {
		if isDisallowedIP(net.ParseIP(s)) {
			t.Fatalf("%s should be allowed", s)
		}
	}
}

func TestHTTPFetcherTruncates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte(strings.Repeat("a", 1000)))
	}))
	defer srv.Close()

	resp, err := testFetcher().Fetch(context.Background(), Request{URL: srv.URL, MaxBytes: 100})
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Truncated || len(resp.Text) != 100 {
		t.Fatalf("truncated=%v len=%d", resp.Truncated, len(resp.Text))
	}
}

func TestHTTPFetcherFollowsRedirect(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/b", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("final"))
	})
	mux.HandleFunc("/a", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/b", http.StatusFound)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := testFetcher().Fetch(context.Background(), Request{URL: srv.URL + "/a"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(resp.URL, "/b") {
		t.Fatalf("final url = %q", resp.URL)
	}
	if resp.Text != "final" {
		t.Fatalf("text = %q", resp.Text)
	}
}

func TestHTTPFetcherBinaryRejected(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write([]byte{0x00, 0x01, 0x02, 0x00})
	}))
	defer srv.Close()

	if _, err := testFetcher().Fetch(context.Background(), Request{URL: srv.URL}); !errors.Is(err, ErrNotText) {
		t.Fatalf("err = %v, want ErrNotText", err)
	}
}

func TestHTTPFetcherCancelled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := testFetcher().Fetch(ctx, Request{URL: srv.URL}); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}
