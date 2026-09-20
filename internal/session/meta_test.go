package session

import (
	"testing"
)

func TestStoreSetMetaAndDelete(t *testing.T) {
	dir := t.TempDir()
	st := NewStore(dir)
	id := "sess-meta-1"
	if err := st.Save(&Record{ID: id, Title: "auto", Entries: []Entry{{Role: "user", Content: "hi"}}}); err != nil {
		t.Fatal(err)
	}

	title := "手动改名"
	note := "这是一条备注"
	if err := st.SetMeta(id, &title, &note); err != nil {
		t.Fatal(err)
	}
	rec, err := st.Load(id)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Title != title || rec.Note != note {
		t.Fatalf("meta = title=%q note=%q", rec.Title, rec.Note)
	}

	// Only note update leaves title intact.
	note2 := "更新备注"
	if err := st.SetMeta(id, nil, &note2); err != nil {
		t.Fatal(err)
	}
	rec, _ = st.Load(id)
	if rec.Title != title || rec.Note != note2 {
		t.Fatalf("partial meta = title=%q note=%q", rec.Title, rec.Note)
	}

	if err := st.Delete(id); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Load(id); err == nil {
		t.Fatal("expected load error after delete")
	}
	// Delete missing is fine.
	if err := st.Delete("no-such-id"); err != nil {
		t.Fatalf("delete missing: %v", err)
	}
}
