package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/kodnificent/berth/internal/apperr"
)

// For the codes the command line cannot trigger on its own: each
// documented error reaches stdout as one JSON object and exits with its documented code.
func TestReportJSONMatchesDocumentedExitCodes(t *testing.T) {
	cases := []struct {
		err      error
		wantCode string
		wantExit int
	}{
		{apperr.New(apperr.Usage, "bad usage"), "usage", 1},
		{apperr.New(apperr.UnknownCommand, "unknown command %q", "x"), "unknown_command", 1},
		{apperr.New(apperr.UnknownFlag, "unknown flag %q", "--x"), "unknown_flag", 1},
		{apperr.New(apperr.NoFreePorts, "no free port in 40000-40999"), "no_free_ports", 2},
		{apperr.New(apperr.WorktreeNotFound, "not in a worktree"), "worktree_not_found", 3},
		{apperr.New(apperr.StateCloneFailed, "mysql clone failed"), "state_clone_failed", 4},
		{apperr.New(apperr.ConfigInvalid, "berth.toml: unknown key"), "config_invalid", 5},
		{apperr.New(apperr.MainWorktree, "the main worktree is the source"), "main_worktree", 1},
		{apperr.New(apperr.NoBerth, "worktree has no berth"), "no_berth", 1},
		{apperr.New(apperr.ConfigExists, "berth.toml already exists"), "config_exists", 1},
		{apperr.New(apperr.ComposeTooOld, "docker compose 2.18.1 misreads compose.berth.yml"), "compose_too_old", 1},
		{errors.New("disk on fire"), "error", 1},
		{fmt.Errorf("wrapped: %w", apperr.New(apperr.ConfigInvalid, "bad")), "config_invalid", 5},
	}
	for _, c := range cases {
		t.Run(c.wantCode, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			exit := report(c.err, true, &stdout, &stderr)
			if exit != c.wantExit {
				t.Errorf("exit = %d, want %d", exit, c.wantExit)
			}
			var got struct {
				Error struct {
					Code    string `json:"code"`
					Message string `json:"message"`
				} `json:"error"`
			}
			dec := json.NewDecoder(&stdout)
			if err := dec.Decode(&got); err != nil {
				t.Fatalf("stdout is not JSON: %v", err)
			}
			if dec.More() {
				t.Fatalf("stdout holds more than one JSON value")
			}
			if got.Error.Code != c.wantCode {
				t.Errorf("error.code = %q, want %q", got.Error.Code, c.wantCode)
			}
			if got.Error.Message == "" {
				t.Errorf("error.message is empty")
			}
			if stderr.Len() != 0 {
				t.Errorf("stderr = %q, want empty in JSON mode", stderr.String())
			}
		})
	}
}

func TestReportHumanGoesToStderr(t *testing.T) {
	var stdout, stderr bytes.Buffer
	exit := report(apperr.New(apperr.WorktreeNotFound, "not in a worktree"), false, &stdout, &stderr)
	if exit != 3 {
		t.Errorf("exit = %d, want 3", exit)
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout = %q, want empty", stdout.String())
	}
	if !bytes.Contains(stderr.Bytes(), []byte("not in a worktree")) {
		t.Errorf("stderr = %q, want the message", stderr.String())
	}
}

func TestReportCarriesWrappedErrorHintsInJSON(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := fmt.Errorf("status: %w", apperr.New(apperr.NoBerth, "no berth").WithNext("run berth add", "then berth status"))
	report(err, true, &stdout, &stderr)
	var got struct {
		Error struct {
			Next []string `json:"next"`
		} `json:"error"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not JSON: %v", err)
	}
	if strings.Join(got.Error.Next, "|") != "run berth add|then berth status" {
		t.Errorf("error.next = %q, want both hints in order", got.Error.Next)
	}
}

func TestReportPrintsHintsAfterTheHumanMessage(t *testing.T) {
	var stdout, stderr bytes.Buffer
	report(apperr.New(apperr.NoBerth, "no berth").WithNext("run berth add"), false, &stdout, &stderr)
	if want := "berth: no berth\nnext: run berth add\n"; stderr.String() != want {
		t.Errorf("stderr = %q, want %q", stderr.String(), want)
	}
}
