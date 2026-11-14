package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"testing"
)

func TestIdleDurationRefusesZeroAndOverflow(t *testing.T) {
	for _, s := range []string{"0d", "0m", "99999999999999999d"} {
		if d, err := parseIdle(s); err == nil {
			t.Errorf("parseIdle(%q) = %s, want an error", s, d)
		}
	}
}

func TestPruneThatFailsPrintsOneJSONObject(t *testing.T) {
	var stdout, stderr bytes.Buffer
	p := &pruner{ctx: &Context{JSON: true, Stdout: &stdout, Stderr: &stderr}, results: []pruneEntry{}, failed: []string{"berth abc: Docker is not reachable"}}
	if exit := finish(p.finish(false), p.ctx); exit != 1 {
		t.Errorf("a prune that failed exits %d, want 1", exit)
	}

	dec := json.NewDecoder(&stdout)
	var out struct {
		Berths []pruneEntry `json:"berths"`
		Error  *errorJSON   `json:"error"`
	}
	if err := dec.Decode(&out); err != nil {
		t.Fatalf("stdout is not JSON: %v", err)
	}
	var extra json.RawMessage
	if err := dec.Decode(&extra); err != io.EOF {
		t.Fatalf("stdout holds a second JSON value: %s", extra)
	}
	if out.Berths == nil || out.Error == nil || out.Error.Code != "error" || !strings.Contains(out.Error.Message, "Docker is not reachable") {
		t.Errorf("the object lacks berths or the error: %+v", out)
	}
}
