package client

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/cloudcons/deconflict-cli/pkg/claim"
)

// The file store had no test of its own until this module was extracted, and
// the extraction found the reason it needed one: it locked with flock, which
// does not exist on Windows, and nothing built for Windows to say so.
func TestFileStoreRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "claims.jsonl")
	st := &FileStore{Path: path}

	if evs, err := st.Events(); err != nil || evs != nil {
		t.Fatalf("empty log: got %v, %v; want nil, nil", evs, err)
	}

	want := []string{"a", "b", "c"}
	for _, id := range want {
		e := claim.Event{Op: "claim", TS: time.Now().UTC(), Claim: claim.Claim{ID: id}}
		if err := st.Append(e); err != nil {
			t.Fatalf("append %s: %v", id, err)
		}
	}

	got, err := st.Events()
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("read back %d events, want %d", len(got), len(want))
	}
	for i, id := range want {
		if got[i].Claim.ID != id {
			t.Errorf("event %d is %q, want %q — order is the log's whole contract", i, got[i].Claim.ID, id)
		}
	}
}

// A corrupt line must cost one claim, not the whole log: an unreadable registry
// is an outage, a missing claim is a missed warning.
func TestFileStoreSkipsUnparseableLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "claims.jsonl")
	st := &FileStore{Path: path}
	if err := st.Append(claim.Event{Op: "claim", Claim: claim.Claim{ID: "before"}}); err != nil {
		t.Fatal(err)
	}
	fh, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fh.WriteString("{ this is not json\n"); err != nil {
		t.Fatal(err)
	}
	fh.Close()
	if err := st.Append(claim.Event{Op: "claim", Claim: claim.Claim{ID: "after"}}); err != nil {
		t.Fatal(err)
	}

	got, err := st.Events()
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	if len(got) != 2 || got[0].Claim.ID != "before" || got[1].Claim.ID != "after" {
		t.Fatalf("got %d events %v, want the two readable ones", len(got), got)
	}
}
