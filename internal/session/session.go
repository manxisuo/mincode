// Package session persists conversation state for --continue.
package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/manxisuo/mincode/internal/llm"
)

// Entry is one stored conversation message with provenance.
type Entry struct {
	Role       string         `json:"role"`
	Content    string         `json:"content"`
	ToolCalls  []llm.ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
	Source     string         `json:"source"`
	Tokens     int            `json:"tokens"`
}

// Record is the on-disk session document.
type Record struct {
	ID           string    `json:"id"`
	Title        string    `json:"title,omitempty"`
	Workspace    string    `json:"workspace"`
	Provider     string    `json:"provider"`
	Model        string    `json:"model"`
	SystemPrompt string    `json:"system_prompt"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
	Entries      []Entry   `json:"entries"`
	Turns        int       `json:"turns"`
}

// Store reads/writes session JSON files under a directory.
type Store struct {
	Dir string
}

// NewStore creates a store rooted at dir (usually .mincode/sessions).
func NewStore(dir string) *Store {
	return &Store{Dir: dir}
}

func (s *Store) path(id string) string {
	return filepath.Join(s.Dir, id+".json")
}

// Save writes the record atomically-ish (write temp + rename).
func (s *Store) Save(rec *Record) error {
	if rec.ID == "" {
		return fmt.Errorf("session id required")
	}
	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		return err
	}
	rec.UpdatedAt = time.Now().UTC()
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path(rec.ID) + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path(rec.ID))
}

// Load reads one session by id.
func (s *Store) Load(id string) (*Record, error) {
	data, err := os.ReadFile(s.path(id))
	if err != nil {
		return nil, err
	}
	var rec Record
	if err := json.Unmarshal(data, &rec); err != nil {
		return nil, fmt.Errorf("parse session %s: %w", id, err)
	}
	return &rec, nil
}

// SetTitle updates the display title of a saved session.
func (s *Store) SetTitle(id, title string) error {
	rec, err := s.Load(id)
	if err != nil {
		return err
	}
	rec.Title = title
	return s.Save(rec)
}

// Latest returns the most recently updated session for a workspace.
// If workspace is empty, returns the global latest.
func (s *Store) Latest(workspace string) (*Record, error) {
	recs, err := s.List()
	if err != nil {
		return nil, err
	}
	for i := len(recs) - 1; i >= 0; i-- {
		if workspace == "" || sameWorkspace(recs[i].Workspace, workspace) {
			return &recs[i], nil
		}
	}
	return nil, fmt.Errorf("no session found for workspace %s", workspace)
}

// List returns sessions sorted by UpdatedAt ascending.
func (s *Store) List() ([]Record, error) {
	entries, err := os.ReadDir(s.Dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []Record
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		id := strings.TrimSuffix(e.Name(), ".json")
		rec, err := s.Load(id)
		if err != nil {
			continue
		}
		out = append(out, *rec)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].UpdatedAt.Equal(out[j].UpdatedAt) {
			return out[i].UpdatedAt.Before(out[j].UpdatedAt)
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

func sameWorkspace(a, b string) bool {
	ap, err1 := filepath.Abs(a)
	bp, err2 := filepath.Abs(b)
	if err1 != nil || err2 != nil {
		return filepath.Clean(a) == filepath.Clean(b)
	}
	return strings.EqualFold(ap, bp)
}

// DefaultDir returns .mincode/sessions under root.
func DefaultDir(root string) string {
	return filepath.Join(root, ".mincode", "sessions")
}
