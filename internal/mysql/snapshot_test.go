package mysql

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"testing"
)

func TestSnapshotFramesRoundTripAndATruncatedStreamFails(t *testing.T) {
	var b bytes.Buffer
	if err := writeFrame(&b, frameTrigger, "audit", "ANSI_QUOTES", "CREATE TRIGGER t"); err != nil {
		t.Fatal(err)
	}
	r := bufio.NewReader(bytes.NewReader(b.Bytes()))
	kind, name, extra, body, err := readFrame(r)
	if err != nil || kind != frameTrigger || name != "audit" || extra != "ANSI_QUOTES" || body != "CREATE TRIGGER t" {
		t.Fatalf("frame read back as %q %q %q %q, err %v", kind, name, extra, body, err)
	}
	if _, _, _, _, err := readFrame(r); !errors.Is(err, io.EOF) {
		t.Errorf("end of stream: %v, want io.EOF", err)
	}
	partial := bufio.NewReader(bytes.NewReader(b.Bytes()[:b.Len()-3]))
	if _, _, _, _, err := readFrame(partial); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Errorf("a stream cut short mid-frame: %v, want an error, never a partial restore", err)
	}
}
