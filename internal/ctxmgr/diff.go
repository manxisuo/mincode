package ctxmgr

import "fmt"

// DiffChange classifies one item between two context snapshots.
type DiffChange string

const (
	DiffAdded          DiffChange = "added"
	DiffRemoved        DiffChange = "removed"
	DiffKept           DiffChange = "kept"
	DiffExcludedNow    DiffChange = "excluded_now"
	DiffTruncatedNow   DiffChange = "truncated_now"
	DiffReincluded     DiffChange = "reincluded"
	DiffStillExcluded  DiffChange = "still_excluded"
	DiffStatusRestored DiffChange = "restored_from_summary"
)

// DiffEntry is one line in a context snapshot diff.
type DiffEntry struct {
	Change  DiffChange `json:"change"`
	Source  Source     `json:"source"`
	Role    string     `json:"role,omitempty"`
	Preview string     `json:"preview,omitempty"`
	Tokens  int        `json:"tokens,omitempty"`
	Reason  string     `json:"reason,omitempty"`
	Policy  string     `json:"policy,omitempty"`
	Saved   int        `json:"saved_tokens,omitempty"`
}

// SnapshotDiff compares two consecutive context builds (T-obs-2).
type SnapshotDiff struct {
	FromStep     int `json:"from_step"`
	ToStep       int `json:"to_step"`
	Added        int `json:"added"`
	Removed      int `json:"removed"`
	Kept         int `json:"kept"`
	ExcludedNow  int `json:"excluded_now"`
	TruncatedNow int `json:"truncated_now"`
	Reincluded   int `json:"reincluded"`
	SavedTokens  int `json:"saved_tokens"`
	FromTotal    int `json:"from_total_tokens"`
	ToTotal      int `json:"to_total_tokens"`
	Budget       int `json:"budget"`
	// Notes are human-readable causal explanations (Runtime decisions).
	Notes   []string    `json:"notes,omitempty"`
	Entries []DiffEntry `json:"entries,omitempty"`
}

// itemKey matches logical context items across snapshots.
func itemKey(it Item) string {
	return string(it.Source) + "|" + it.Role + "|" + it.ToolCallID + "|" + previewKey(it.Preview)
}

func previewKey(s string) string {
	r := []rune(s)
	if len(r) > 48 {
		r = r[:48]
	}
	return string(r)
}

// DiffSnapshots explains what changed from prev → next and why.
// prev may be nil (first build).
func DiffSnapshots(prev, next *Snapshot) *SnapshotDiff {
	if next == nil {
		return nil
	}
	d := &SnapshotDiff{
		ToStep:      next.Step,
		ToTotal:     next.TotalTokens,
		Budget:      next.Budget,
		ExcludedNow: 0,
	}
	if prev == nil {
		d.Notes = []string{"first context build of the turn/session"}
		for _, it := range next.Items {
			if it.Excluded || it.Truncated {
				d.Entries = append(d.Entries, diffEntryFrom(it))
				if it.Excluded {
					d.ExcludedNow++
				}
				if it.Truncated {
					d.TruncatedNow++
				}
				d.SavedTokens += it.savedTokens()
			}
		}
		return d
	}
	d.FromStep = prev.Step
	d.FromTotal = prev.TotalTokens

	prevByKey := map[string]Item{}
	for _, it := range prev.Items {
		prevByKey[itemKey(it)] = it
	}
	seen := map[string]bool{}

	for _, it := range next.Items {
		k := itemKey(it)
		seen[k] = true
		old, ok := prevByKey[k]
		if !ok {
			if it.Excluded {
				d.ExcludedNow++
				d.SavedTokens += it.savedTokens()
				d.Entries = append(d.Entries, diffEntryFrom(it))
			} else {
				d.Added++
				d.Entries = append(d.Entries, diffEntryFrom(it))
			}
			continue
		}
		// Matched item.
		switch {
		case it.Excluded && !old.Excluded:
			d.ExcludedNow++
			d.SavedTokens += it.savedTokens()
			d.Entries = append(d.Entries, diffEntryFrom(it))
		case it.Truncated && !old.Truncated:
			d.TruncatedNow++
			d.SavedTokens += it.savedTokens()
			d.Entries = append(d.Entries, diffEntryFrom(it))
		case !it.Excluded && old.Excluded:
			d.Reincluded++
			e := diffEntryFrom(it)
			e.Change = DiffReincluded
			d.Entries = append(d.Entries, e)
		case it.Excluded && old.Excluded:
			// omit from list to reduce noise
		default:
			d.Kept++
		}
	}
	for _, it := range prev.Items {
		k := itemKey(it)
		if seen[k] {
			continue
		}
		d.Removed++
		e := diffEntryFrom(it)
		e.Change = DiffRemoved
		d.Entries = append(d.Entries, e)
	}

	// Causal notes (Runtime policy).
	if d.ExcludedNow > 0 {
		d.Notes = append(d.Notes, fmt.Sprintf(
			"budget: msg tokens projected over limit; policy=drop_oldest_non_pinned excluded %d item(s), saved≈%d tokens (budget=%d)",
			d.ExcludedNow, d.SavedTokens, d.Budget))
	}
	if d.TruncatedNow > 0 {
		d.Notes = append(d.Notes, fmt.Sprintf(
			"tool result budget: truncated %d tool_result item(s), saved≈%d tokens (policy=tool_result_max_tokens)",
			d.TruncatedNow, d.SavedTokens))
	}
	if d.Removed > 0 && d.FromTotal > d.ToTotal {
		d.Notes = append(d.Notes, fmt.Sprintf(
			"items dropped vs previous build: %d (compaction or history rewrite); total %d → %d",
			d.Removed, d.FromTotal, d.ToTotal))
	}
	if len(d.Notes) == 0 {
		if d.Added > 0 {
			d.Notes = append(d.Notes, fmt.Sprintf("added %d item(s); no budget pressure", d.Added))
		} else {
			d.Notes = append(d.Notes, "no context budget action this build")
		}
	}
	return d
}

func diffEntryFrom(it Item) DiffEntry {
	e := DiffEntry{
		Source:  it.Source,
		Role:    it.Role,
		Preview: it.Preview,
		Tokens:  it.Tokens,
		Reason:  it.Reason,
		Policy:  it.Policy,
		Saved:   it.savedTokens(),
		Change:  DiffKept,
	}
	switch {
	case it.Excluded:
		e.Change = DiffExcludedNow
		e.Saved = it.savedTokens()
	case it.Truncated:
		e.Change = DiffTruncatedNow
		e.Saved = it.savedTokens()
	}
	return e
}

func (it Item) savedTokens() int {
	if it.SavedTokens > 0 {
		return it.SavedTokens
	}
	if it.Excluded {
		return it.OrigTokens
	}
	return 0
}
