package permission

import "testing"

func TestDefaultPolicy(t *testing.T) {
	p := NewDefaultPolicy()
	if p.Evaluate(Request{Tool: "read_file"}) != Allow {
		t.Fatal("read should allow")
	}
	if p.Evaluate(Request{Tool: "write_file"}) != Ask {
		t.Fatal("write should ask")
	}
	if p.Evaluate(Request{Tool: "edit_file"}) != Ask {
		t.Fatal("edit should ask")
	}
	if p.Evaluate(Request{Tool: "rm_rf"}) != Deny {
		t.Fatal("unknown should deny")
	}
	if p.Evaluate(Request{Tool: "web_search"}) != Allow {
		t.Fatal("web_search should allow (read-only, backend must be configured)")
	}
}

func TestEvaluateAllowDenyAsk(t *testing.T) {
	p := NewDefaultPolicy()

	denied, err := Evaluate(p, nil, Request{Tool: "read_file"})
	if denied || err != nil {
		t.Fatalf("allow: %v %v", denied, err)
	}

	denied, err = Evaluate(p, nil, Request{Tool: "nope"})
	if !denied || err == nil {
		t.Fatalf("deny: %v %v", denied, err)
	}

	// Ask without approver → denied
	denied, err = Evaluate(p, nil, Request{Tool: "write_file"})
	if !denied || err == nil {
		t.Fatalf("ask no approver: %v %v", denied, err)
	}

	yes := stubApprover{ok: true}
	denied, err = Evaluate(p, yes, Request{Tool: "write_file"})
	if denied || err != nil {
		t.Fatalf("ask yes: %v %v", denied, err)
	}

	no := stubApprover{ok: false}
	denied, err = Evaluate(p, no, Request{Tool: "write_file"})
	if !denied || err == nil {
		t.Fatalf("ask no: %v %v", denied, err)
	}
}

type stubApprover struct {
	ok  bool
	err error
}

func (s stubApprover) Approve(req Request) (bool, error) { return s.ok, s.err }
