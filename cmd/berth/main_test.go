package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

var berthBin string

// exe is the suffix Windows runs a program by.
var exe = func() string {
	if runtime.GOOS == "windows" {
		return ".exe"
	}
	return ""
}()

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "berth-bin-")
	if err != nil {
		panic(err)
	}
	berthBin = filepath.Join(dir, "berth"+exe)
	build := exec.Command("go", "build", "-o", berthBin, ".")
	build.Stdout, build.Stderr = os.Stderr, os.Stderr
	if err := build.Run(); err != nil {
		os.RemoveAll(dir)
		panic("building berth: " + err.Error())
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

type result struct {
	stdout, stderr string
	exit           int
}

func runBerth(t *testing.T, args ...string) result {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, berthBin, args...)
	cmd.Dir = t.TempDir()
	cmd.Env = append(os.Environ(), "BERTH_STATE_DIR="+t.TempDir())
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	if ctx.Err() != nil {
		t.Fatalf("berth %v did not exit within 10s", args)
	}
	exit := 0
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		exit = exitErr.ExitCode()
	} else if err != nil {
		t.Fatalf("running berth %v: %v", args, err)
	}
	return result{stdout.String(), stderr.String(), exit}
}

func TestVersionComesFromBuild(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "berth"+exe)
	build := exec.Command("go", "build", "-ldflags", "-X main.version=9.8.7-test", "-o", bin, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	out, err := exec.Command(bin, "--version").Output()
	if err != nil {
		t.Fatalf("berth --version: %v", err)
	}
	if !strings.Contains(string(out), "9.8.7-test") {
		t.Fatalf("stdout = %q, want it to contain the stamped version 9.8.7-test", out)
	}
}

func TestVersionJSON(t *testing.T) {
	r := runBerth(t, "--version", "--json")
	if r.exit != 0 {
		t.Fatalf("exit = %d, want 0", r.exit)
	}
	var v struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal([]byte(r.stdout), &v); err != nil || v.Version == "" {
		t.Fatalf("stdout = %q, want {\"version\": ...}", r.stdout)
	}
}

// An unknown command exits 1 and names the command it didn't recognise.
func TestUnknownCommandExitsOneAndNamesIt(t *testing.T) {
	r := runBerth(t, "frobnicate")
	if r.exit != 1 {
		t.Fatalf("exit = %d, want 1", r.exit)
	}
	if !strings.Contains(r.stdout+r.stderr, "frobnicate") {
		t.Fatalf("output does not name the command; stdout %q, stderr %q", r.stdout, r.stderr)
	}
}

// documentedExit is the exit code table from docs/design.md, Commands > Conventions,
// keyed by the stable error.code berth reports for it.
var documentedExit = map[string]int{
	"unknown_command":    1,
	"unknown_flag":       1,
	"usage":              1,
	"error":              1,
	"no_free_ports":      2,
	"worktree_not_found": 3,
	"state_clone_failed": 4,
	"config_invalid":     5,
	"main_worktree":      1,
	"no_berth":           1,
}

type jsonError struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func decodeOneObject(t *testing.T, s string) jsonError {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(s))
	var raw map[string]json.RawMessage
	if err := dec.Decode(&raw); err != nil {
		t.Fatalf("stdout is not a JSON object: %v; stdout %q", err, s)
	}
	if _, err := dec.Token(); err != io.EOF {
		t.Fatalf("stdout holds more than one JSON value: %q", s)
	}
	var e jsonError
	if err := json.Unmarshal([]byte(s), &e); err != nil {
		t.Fatalf("decoding error object: %v", err)
	}
	return e
}

// With --json, every error is one JSON object on stdout with a stable
// error.code and a human error.message, and the exit code matches the documented code.
func TestJSONErrors(t *testing.T) {
	cases := []struct {
		name     string
		args     []string
		wantCode string
		mention  string
	}{
		{"unknown command, flag first", []string{"--json", "frobnicate"}, "unknown_command", "frobnicate"},
		{"unknown command, flag last", []string{"frobnicate", "--json"}, "unknown_command", "frobnicate"},
		{"unknown global flag", []string{"--json", "--frobnicate"}, "unknown_flag", "--frobnicate"},
		{"no command", []string{"--json"}, "usage", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := runBerth(t, c.args...)
			e := decodeOneObject(t, r.stdout)
			if e.Error.Code != c.wantCode {
				t.Errorf("error.code = %q, want %q", e.Error.Code, c.wantCode)
			}
			if strings.TrimSpace(e.Error.Message) == "" {
				t.Errorf("error.message is empty")
			}
			if c.mention != "" && !strings.Contains(e.Error.Message, c.mention) {
				t.Errorf("error.message = %q, want it to mention %q", e.Error.Message, c.mention)
			}
			want, ok := documentedExit[e.Error.Code]
			if !ok {
				t.Fatalf("error.code %q is not a documented code", e.Error.Code)
			}
			if r.exit != want {
				t.Errorf("exit = %d, want %d for %s", r.exit, want, e.Error.Code)
			}
			if r.stderr != "" {
				t.Errorf("stderr = %q, want nothing outside the JSON object", r.stderr)
			}
		})
	}
}

// Without a TTY, no command ever waits for input. Every command is run
// with stdin an open pipe that never delivers data or EOF; reading it would block.
func TestNoCommandWaitsForInputWithoutTTY(t *testing.T) {
	invocations := [][]string{
		{},
		{"--help"},
		{"--version"},
		{"frobnicate"},
		{"add"},
		{"remove"},
		{"list"},
		{"status"},
		{"env"},
		{"exec", "--", "true"},
		{"prune"},
		{"doctor"},
		{"init"},
		{"hook", "install"},
		{"config", "path"},
		{"config", "edit"},
	}
	for _, args := range invocations {
		for _, extra := range [][]string{nil, {"--json"}} {
			args := append(append([]string{}, args...), extra...)
			t.Run(strings.Join(append([]string{"berth"}, args...), " "), func(t *testing.T) {
				t.Parallel()
				r, w, err := os.Pipe()
				if err != nil {
					t.Fatal(err)
				}
				defer r.Close()
				defer w.Close()

				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				cmd := exec.CommandContext(ctx, berthBin, args...)
				cmd.Dir = t.TempDir()
				cmd.Env = append(os.Environ(), "BERTH_STATE_DIR="+t.TempDir())
				cmd.Stdin = r
				cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
				_ = cmd.Run()
				if ctx.Err() != nil {
					t.Fatalf("berth %v was still running after 5s with no TTY", args)
				}
			})
		}
	}
}

// Every command prints its own help for -h or --help, wherever the flag sits, and exits
// 0 without running.
func TestEveryCommandPrintsHelp(t *testing.T) {
	r := runBerth(t, "--help", "--json")
	var list struct {
		Commands []struct {
			Name string `json:"name"`
		} `json:"commands"`
	}
	if err := json.Unmarshal([]byte(r.stdout), &list); err != nil || len(list.Commands) == 0 {
		t.Fatalf("berth --help --json = %q, want a list of commands", r.stdout)
	}
	for _, c := range list.Commands {
		for _, args := range [][]string{{c.Name, "-h"}, {c.Name, "--help"}, {c.Name, "some/path", "--help"}} {
			r := runBerth(t, args...)
			if r.exit != 0 {
				t.Errorf("berth %s: exit %d, want 0; stderr: %s", strings.Join(args, " "), r.exit, r.stderr)
			}
			if !strings.Contains(r.stdout, "Usage: berth "+c.Name) {
				t.Errorf("berth %s printed no usage for %s:\n%s", strings.Join(args, " "), c.Name, r.stdout)
			}
		}
	}
	r = runBerth(t, "status", "--help", "--json")
	var help struct {
		Command string `json:"command"`
		Usage   string `json:"usage"`
	}
	if err := json.Unmarshal([]byte(r.stdout), &help); err != nil || help.Command != "status" || !strings.Contains(help.Usage, "--wait") {
		t.Errorf("berth status --help --json = %q, want the command and its usage", r.stdout)
	}
}
