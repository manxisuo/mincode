package experiment

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// NewStore re-joins workspace/.mincode/experiments; NewStoreAt is the root.
func TestStoreRootKinds(t *testing.T) {
	ws := t.TempDir()
	fromWS, err := NewStore(ws)
	if err != nil {
		t.Fatal(err)
	}
	wantNested := filepath.Join(ws, ".mincode", DirName)
	if fromWS.Root() != wantNested {
		t.Fatalf("NewStore root = %q want %q", fromWS.Root(), wantNested)
	}

	// Passing an already-resolved experiments root must not nest again.
	resolved := filepath.Join(t.TempDir(), "projects", "demo", DirName)
	fromRoot, err := NewStoreAt(resolved)
	if err != nil {
		t.Fatal(err)
	}
	if fromRoot.Root() != resolved {
		t.Fatalf("NewStoreAt root = %q want %q", fromRoot.Root(), resolved)
	}
	if strings.Contains(fromRoot.Root(), ".mincode"+string(filepath.Separator)+DirName) {
		t.Fatalf("NewStoreAt nested unexpectedly: %q", fromRoot.Root())
	}
}

func TestStoreSaveLoadSummarize(t *testing.T) {
	st, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	r1 := RunResult{
		RunID: "r1", Experiment: "base", Task: "analyze", Model: "m1",
		Success: true, Steps: 4, ToolCalls: 3, LLMCalls: 5,
		InputTokens: 100, OutputTokens: 50, TotalTokens: 150,
		DurationMS: 1000, FinishedAt: time.Now().UTC(),
	}
	r2 := RunResult{
		RunID: "r2", Experiment: "base", Task: "analyze", Model: "m1",
		Success: false, Steps: 2, ToolCalls: 1, LLMCalls: 2,
		InputTokens: 80, OutputTokens: 20, TotalTokens: 100,
		DurationMS: 500, Failures: 1,
	}
	if err := st.Save(r1); err != nil {
		t.Fatal(err)
	}
	if err := st.Save(r2); err != nil {
		t.Fatal(err)
	}

	runs, err := st.LoadExperiment("base")
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 2 {
		t.Fatalf("runs = %d", len(runs))
	}

	agg := Summarize("base", runs)
	if agg.Runs != 2 || agg.Successes != 1 {
		t.Fatalf("agg = %+v", agg)
	}
	if agg.AvgTotalTok != 125 {
		t.Fatalf("avg total = %v", agg.AvgTotalTok)
	}
	if agg.StepsStat.Min != 2 || agg.StepsStat.Max != 4 || agg.StepsStat.Median != 3 {
		t.Fatalf("steps stat = %+v", agg.StepsStat)
	}
	if agg.TotalFailures != 1 {
		t.Fatalf("failures = %d", agg.TotalFailures)
	}

	names, err := st.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 1 || names[0] != "base" {
		t.Fatalf("names = %v", names)
	}

	// File layout
	if _, err := os.Stat(filepath.Join(st.Root(), "base", "r1.json")); err != nil {
		t.Fatal(err)
	}
}

func TestFormatReports(t *testing.T) {
	st, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	_ = st.Save(RunResult{RunID: "a1", Experiment: "A", Model: "m", Success: true, Steps: 3, TotalTokens: 200, DurationMS: 10})
	_ = st.Save(RunResult{RunID: "b1", Experiment: "B", Model: "m2", Success: true, Steps: 5, TotalTokens: 400, DurationMS: 20})

	list, err := FormatList(st)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(list, "A") || !strings.Contains(list, "B") {
		t.Fatalf("list = %q", list)
	}
	if !strings.Contains(list, "med_steps") {
		t.Fatalf("list missing median: %q", list)
	}

	show, err := FormatShow(st, "A")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(show, "a1") || !strings.Contains(show, "median") {
		t.Fatalf("show = %q", show)
	}

	cmp, err := FormatCompare(st, "A", "B")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(cmp, "steps median") || !strings.Contains(cmp, "total tok median") {
		t.Fatalf("cmp = %q", cmp)
	}
}

func TestStatMedianEvenOdd(t *testing.T) {
	odd := statOf([]float64{3, 1, 2})
	if odd.Median != 2 || odd.Min != 1 || odd.Max != 3 {
		t.Fatalf("odd = %+v", odd)
	}
	even := statOf([]float64{8, 2, 4, 6})
	if even.Median != 5 {
		t.Fatalf("even median = %v", even.Median)
	}
}

func TestLoadMissing(t *testing.T) {
	st, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.LoadExperiment("nope"); err == nil {
		t.Fatal("expected not found")
	}
}
