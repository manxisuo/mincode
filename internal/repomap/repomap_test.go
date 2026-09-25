package repomap

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeFile(t *testing.T, root, rel, content string) {
	t.Helper()
	abs := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestBuildExtractsGoSymbolsAndSkipsVendor(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "main.go", `package main

const Answer = 42
var global = 1

type Server struct{ Addr string }

func main() { s := NewServer(); _ = s; _ = Answer; _ = global }

func NewServer() *Server { return &Server{} }

func (s *Server) Start(port int) error { return nil }
`)
	writeFile(t, root, "pkg/util/util.go", `package util

type Helper interface{ Help() }

func Assist(h Helper) string { return h.Help() }

const hidden = 1
`)
	writeFile(t, root, "vendor/dep/dep.go", `package dep

func ShouldNotAppear() {}
`)
	writeFile(t, root, "node_modules/x.js", `export function nope() {}`)

	m, err := Build(context.Background(), root, Options{MaxTokens: 2000})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"main.go", "NewServer", "Server", "Start", "Answer", "pkg/util/util.go", "Assist", "Helper", "func main("} {
		if !strings.Contains(m.Text, want) {
			t.Fatalf("map missing %q:\n%s", want, m.Text)
		}
	}
	if strings.Contains(m.Text, "func mainfunc") {
		t.Fatalf("duplicated func keyword in signature:\n%s", m.Text)
	}
	for _, bad := range []string{"ShouldNotAppear", "node_modules", "vendor/dep"} {
		if strings.Contains(m.Text, bad) {
			t.Fatalf("map should not contain %q:\n%s", bad, m.Text)
		}
	}
	if len(m.Files) < 2 {
		t.Fatalf("files = %d, want >= 2", len(m.Files))
	}
}

func TestBuildFocusBoosts(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "a/alpha.go", "package a\n\nfunc Alpha() {}\n")
	writeFile(t, root, "b/beta.go", "package b\n\nfunc Beta() {}\n")

	m, err := Build(context.Background(), root, Options{MaxTokens: 2000, Focus: "beta"})
	if err != nil {
		t.Fatal(err)
	}
	iBeta := strings.Index(m.Text, "b/beta.go")
	iAlpha := strings.Index(m.Text, "a/alpha.go")
	if iBeta < 0 || iAlpha < 0 {
		t.Fatalf("both files expected:\n%s", m.Text)
	}
	if iBeta > iAlpha {
		t.Fatalf("focus should rank beta.go first:\n%s", m.Text)
	}
}

func TestBuildSubpathLimitsScan(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "sub/inside.go", "package sub\n\nfunc Inside() {}\n")
	writeFile(t, root, "other/outside.go", "package other\n\nfunc Outside() {}\n")

	m, err := Build(context.Background(), root, Options{MaxTokens: 2000, Subpath: "sub"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(m.Text, "sub/inside.go") {
		t.Fatalf("subpath file missing:\n%s", m.Text)
	}
	if strings.Contains(m.Text, "Outside") {
		t.Fatalf("outside file leaked into subpath map:\n%s", m.Text)
	}
}

func TestBuildRejectsSubpathEscape(t *testing.T) {
	root := t.TempDir()
	if _, err := Build(context.Background(), root, Options{Subpath: "../evil"}); err == nil {
		t.Fatal("expected escape rejection")
	}
}

func TestBuildRespectsTokenBudget(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 40; i++ {
		name := filepath.Join("pkg", strings.Repeat("x", i%5)+string(rune('a'+i%26)), "file"+string(rune('a'+i%26))+".go")
		writeFile(t, root, name, "package p\n\nfunc ExportedFunctionName() {}\nfunc AnotherExported() {}\n")
	}
	m, err := Build(context.Background(), root, Options{MaxTokens: 120})
	if err != nil {
		t.Fatal(err)
	}
	if !m.Truncated {
		t.Fatalf("expected truncation with tiny budget, tokens=%d\n%s", m.Tokens, m.Text)
	}
	if m.Tokens > 200 {
		t.Fatalf("map too large for budget: %d tokens", m.Tokens)
	}
}

func TestBuildContextCancelled(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "a.go", "package a\n")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Build(ctx, root, Options{}); err == nil {
		t.Fatal("expected context error")
	}
}

func TestBuildEmptyWorkspace(t *testing.T) {
	m, err := Build(context.Background(), t.TempDir(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(m.Text, "no code files found") {
		t.Fatalf("text = %q", m.Text)
	}
}

func TestBuildIsBoundedInTime(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 500; i++ {
		writeFile(t, root, filepath.Join("p", string(rune('a'+i%26)), "f"+time.Now().Format("150405")+string(rune('0'+i%10))+".go"), "package p\n\nfunc F() {}\n")
	}
	m, err := Build(context.Background(), root, Options{MaxTokens: 2000})
	if err != nil {
		t.Fatal(err)
	}
	if m.Scanned == 0 {
		t.Fatal("expected scanned files")
	}
}

func TestLangAndGenerated(t *testing.T) {
	cases := map[string]string{
		"main.go":      "go",
		"app.tsx":      "ts",
		"script.py":    "py",
		"Makefile":     "make",
		"Dockerfile":   "docker",
		"go.mod":       "gomod",
		"README.md":    "",
		"data.bin":     "",
		"schema.proto": "proto",
	}
	for name, want := range cases {
		if got := langForName(name); got != want {
			t.Fatalf("langForName(%q) = %q, want %q", name, got, want)
		}
	}
	if !isGenerated("api.pb.go") || !isGenerated("zz_generated.deepcopy.go") {
		t.Fatal("expected generated files detected")
	}
	if isGenerated("main.go") {
		t.Fatal("main.go is not generated")
	}
}
