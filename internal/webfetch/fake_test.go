package webfetch

import (
	"context"
	"errors"
	"testing"
)

func TestFakeFetcherScriptedOrder(t *testing.T) {
	f := NewFakeFetcher(
		Response{Text: "first"},
		Response{Text: "second"},
	)
	r1, err := f.Fetch(context.Background(), Request{URL: "https://a.example"})
	if err != nil {
		t.Fatal(err)
	}
	if r1.Text != "first" || r1.URL != "https://a.example" {
		t.Fatalf("r1 = %+v", r1)
	}
	r2, _ := f.Fetch(context.Background(), Request{URL: "https://b.example"})
	if r2.Text != "second" {
		t.Fatalf("r2 = %+v", r2)
	}
	// Last response repeats.
	r3, _ := f.Fetch(context.Background(), Request{URL: "https://c.example"})
	if r3.Text != "second" {
		t.Fatalf("r3 = %+v", r3)
	}
	if f.Calls() != 3 {
		t.Fatalf("calls = %d", f.Calls())
	}
}

func TestFakeFetcherEmptyURL(t *testing.T) {
	f := NewFakeFetcher(Response{})
	if _, err := f.Fetch(context.Background(), Request{URL: " "}); !errors.Is(err, ErrEmptyURL) {
		t.Fatalf("err = %v, want ErrEmptyURL", err)
	}
}

func TestFakeFetcherError(t *testing.T) {
	f := NewFakeFetcher(Response{})
	f.Err = errors.New("forced")
	if _, err := f.Fetch(context.Background(), Request{URL: "https://x"}); err == nil {
		t.Fatal("expected error")
	}
}

func TestFakeFetcherCancelled(t *testing.T) {
	f := NewFakeFetcher(Response{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := f.Fetch(ctx, Request{URL: "https://x"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
}

func TestFakeFetcherRecordsRequests(t *testing.T) {
	f := NewFakeFetcher(Response{})
	_, _ = f.Fetch(context.Background(), Request{URL: "https://one", MaxBytes: 10})
	_, _ = f.Fetch(context.Background(), Request{URL: "https://two"})
	reqs := f.Requests()
	if len(reqs) != 2 || reqs[0].URL != "https://one" || reqs[0].MaxBytes != 10 {
		t.Fatalf("reqs = %+v", reqs)
	}
}
