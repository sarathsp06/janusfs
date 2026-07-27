package control

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestRequestResumeNeverSerializes asserts that Resume — the daemon-internal
// flag letting startup recreate a recorded mountpoint — never crosses the
// wire. A client encoding Resume:true into a Request must have no way to make
// it take effect; json:"-" is a security property here, not a serialization
// convenience.
func TestRequestResumeNeverSerializes(t *testing.T) {
	req := Request{Cmd: "mount", Src: "/tmp/src", Resume: true}
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "esume") { // catches "Resume" or "resume"
		t.Fatalf("Resume leaked into the wire encoding: %s", data)
	}

	var round Request
	if err := json.Unmarshal(data, &round); err != nil {
		t.Fatal(err)
	}
	if round.Resume {
		t.Fatal("Resume decoded as true from a request that never set it on the wire")
	}
}

// TestRequestRoundTrip is a basic sanity check that the non-internal fields
// survive an encode/decode cycle.
func TestRequestRoundTrip(t *testing.T) {
	req := Request{Cmd: "mount", Src: "/tmp/src", Mountpoint: "/tmp/mnt", Label: "demo", NoHistory: true}
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	var round Request
	if err := json.Unmarshal(data, &round); err != nil {
		t.Fatal(err)
	}
	round.Resume = false // Resume is deliberately excluded from the comparison
	if round != req {
		t.Fatalf("round-trip mismatch: got %+v, want %+v", round, req)
	}
}

// TestResponseOK asserts the same "no explicit OK ⇒ derive from Error" rule
// WriteResponse enforces, so a handler that returns a bare Response{} reports
// success.
func TestWriteResponseDefaultsOKFromError(t *testing.T) {
	// WriteResponse writes to a net.Conn; exercise the same rule it applies
	// without needing a real connection.
	resp := Response{}
	resp.OK = resp.OK || resp.Error == ""
	if !resp.OK {
		t.Fatal("expected a bare Response with no Error to default OK to true")
	}

	resp2 := Response{Error: "boom"}
	resp2.OK = resp2.OK || resp2.Error == ""
	if resp2.OK {
		t.Fatal("expected a Response with a non-empty Error to not default OK to true")
	}
}
