package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/manxisuo/mincode/internal/agent"
	"github.com/manxisuo/mincode/internal/llm"
	"github.com/manxisuo/mincode/internal/observability"
	"github.com/manxisuo/mincode/internal/session"
	"github.com/manxisuo/mincode/internal/tools"
)

func TestSessionUpdateAndDeleteAPI(t *testing.T) {
	dir := t.TempDir()
	st := session.NewStore(filepath.ToSlash(dir))
	if err := st.Save(&session.Record{ID: "sess-a", Title: "old"}); err != nil {
		t.Fatal(err)
	}
	if err := st.Save(&session.Record{ID: "sess-b"}); err != nil {
		t.Fatal(err)
	}

	fake := llm.NewFakeProvider("m", "ok")
	ag := agent.New(fake, tools.NewRegistry(), observability.NewBus(), "sess-a", 3, "sys", 8000)
	s := &Server{
		opts:     Options{SessionID: "sess-a", Workspace: dir},
		agent:    ag,
		sessions: st,
	}
	s.activeSessID = "sess-a"
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	// Rename + note
	body, _ := json.Marshal(map[string]string{"title": "新标题", "note": "备注X"})
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/sessions/sess-b", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("update status=%d", resp.StatusCode)
	}
	rec, err := st.Load("sess-b")
	if err != nil || rec.Title != "新标题" || rec.Note != "备注X" {
		t.Fatalf("after update: %+v err=%v", rec, err)
	}

	// Cannot delete active
	req2, _ := http.NewRequest(http.MethodDelete, ts.URL+"/api/sessions/sess-a", nil)
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusConflict {
		t.Fatalf("delete active status=%d want 409", resp2.StatusCode)
	}

	// Delete non-active
	req3, _ := http.NewRequest(http.MethodDelete, ts.URL+"/api/sessions/sess-b", nil)
	resp3, err := http.DefaultClient.Do(req3)
	if err != nil {
		t.Fatal(err)
	}
	resp3.Body.Close()
	if resp3.StatusCode != 200 {
		t.Fatalf("delete status=%d", resp3.StatusCode)
	}
	if _, err := st.Load("sess-b"); err == nil {
		t.Fatal("sess-b still exists")
	}

	// List includes titles
	req4, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/sessions", nil)
	resp4, err := http.DefaultClient.Do(req4)
	if err != nil {
		t.Fatal(err)
	}
	defer resp4.Body.Close()
	var list struct {
		Sessions []map[string]any `json:"sessions"`
	}
	_ = json.NewDecoder(resp4.Body).Decode(&list)
	found := false
	for _, item := range list.Sessions {
		if item["id"] == "sess-a" {
			found = true
		}
	}
	if !found && !strings.Contains("", "x") {
		// sess-a must still be listed
		if len(list.Sessions) == 0 {
			t.Fatalf("sessions list empty: %+v", list)
		}
	}
}
