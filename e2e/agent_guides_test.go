//go:build e2e

package e2e

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var agentGuides = []string{"../docs/agents/working-in-a-berth.md", "../docs/agents/setting-up-berth.md"}

func readGuide(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading guide: %v", err)
	}
	if len(bytes.TrimSpace(data)) == 0 {
		t.Fatalf("%s is empty", path)
	}
	return string(data)
}

// runBerthOffline runs berth outside any repository, with every proxy variable pointing
// at a port nothing listens on, so a command that reaches for the network fails.
func runBerthOffline(t *testing.T, args ...string) (stdout, stderr string, exit int) {
	t.Helper()
	cmd := exec.Command(berthBin, args...)
	cmd.Dir = t.TempDir()
	dead := "http://127.0.0.1:9"
	cmd.Env = append(isolatedEnviron(),
		"HTTP_PROXY="+dead, "HTTPS_PROXY="+dead, "ALL_PROXY="+dead,
		"http_proxy="+dead, "https_proxy="+dead, "all_proxy="+dead,
		"NO_PROXY=", "no_proxy=",
		"BERTH_STATE_DIR="+filepath.Join(cmd.Dir, "state"),
		"XDG_CONFIG_HOME="+filepath.Join(cmd.Dir, "config"),
	)
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	err := cmd.Run()
	if ee, ok := err.(*exec.ExitError); ok {
		return out.String(), errOut.String(), ee.ExitCode()
	} else if err != nil {
		t.Fatalf("berth %v: %v", args, err)
	}
	return out.String(), errOut.String(), 0
}

// berth docs agents prints each guide byte for byte, with no network and
// no repository to read them from.
func TestDocsAgentsPrintsTheGuidesVerbatim(t *testing.T) {
	stdout, stderr, exit := runBerthOffline(t, "docs", "agents")
	if exit != 0 {
		t.Fatalf("berth docs agents exited %d\nstderr:\n%s", exit, stderr)
	}
	for _, path := range agentGuides {
		if guide := readGuide(t, path); !strings.Contains(stdout, guide) {
			t.Errorf("berth docs agents does not contain %s byte for byte\nstdout:\n%s", path, stdout)
		}
	}
}

// guideUnits splits markdown into paragraphs and list items, each with its whitespace
// collapsed and lowercased, so a phrase wrapped across lines still matches.
func guideUnits(text string) []string {
	var units []string
	var cur []string
	flush := func() {
		if len(cur) > 0 {
			units = append(units, strings.ToLower(strings.Join(strings.Fields(strings.Join(cur, " ")), " ")))
			cur = nil
		}
	}
	item := regexp.MustCompile(`^\s*([-*+]|\d+[.)])\s`)
	for _, line := range strings.Split(text, "\n") {
		if strings.TrimSpace(line) == "" || item.MatchString(line) || strings.HasPrefix(strings.TrimSpace(line), "#") {
			flush()
		}
		if strings.TrimSpace(line) != "" {
			cur = append(cur, line)
		}
	}
	flush()
	return units
}

var negation = regexp.MustCompile(`\b(never|not|don't|don’t|avoid)\b`)

// The working guide steers an agent through each of the six rules. Each
// check needs the rule's key terms together in one paragraph or list item, not any
// particular wording.
func TestWorkingGuideTellsAnAgentTheRules(t *testing.T) {
	units := guideUnits(readGuide(t, agentGuides[0]))
	rules := []struct {
		rule string
		ok   func(u string) bool
	}{
		{"run berth status first", func(u string) bool {
			return strings.Contains(u, "berth status") && regexp.MustCompile(`\b(first|before)\b`).MatchString(u)
		}},
		{"run berth add only when about to start the app or its tests", func(u string) bool {
			return strings.Contains(u, "berth add") && strings.Contains(u, "test") && regexp.MustCompile(`\b(start|run|only)\b`).MatchString(u)
		}},
		{"use the berth's URL and ports", func(u string) bool {
			return strings.Contains(u, "url") && strings.Contains(u, "port")
		}},
		{"wait for clones with berth status --wait", func(u string) bool {
			return strings.Contains(u, "berth status --wait") && strings.Contains(u, "clon")
		}},
		{"never edit the managed block", func(u string) bool {
			return strings.Contains(u, "managed block") && negation.MatchString(u)
		}},
		{"never pass -p to compose", func(u string) bool {
			return regexp.MustCompile("(^|[\\s`])-p([\\s`]|$)").MatchString(u) && strings.Contains(u, "compose") && negation.MatchString(u)
		}},
	}
	for _, r := range rules {
		found := false
		for _, u := range units {
			if r.ok(u) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("the working guide never tells an agent to %s", r.rule)
		}
	}
}

// berthInvocations returns every berth command line the guides show: the rest of each
// inline code span or fenced code line that starts with berth.
func berthInvocations(text string) []string {
	var code []string
	fenced := false
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			fenced = !fenced
			continue
		}
		if fenced {
			code = append(code, line)
			continue
		}
		for _, m := range regexp.MustCompile("`([^`]+)`").FindAllStringSubmatch(line, -1) {
			code = append(code, m[1])
		}
	}
	var calls []string
	start := regexp.MustCompile(`(?:^|[\s$;&|(])berth\s+(.*)$`)
	for _, c := range code {
		if m := start.FindStringSubmatch(c); m != nil {
			calls = append(calls, m[1])
		}
	}
	return calls
}

// Every command, subcommand and flag the guides name is in the binary's
// help. Tokens after a bare -- belong to the command exec runs, not to berth.
func TestGuidesNameOnlyCommandsAndFlagsInHelp(t *testing.T) {
	topHelp, stderr, exit := runBerthOffline(t, "--help")
	if exit != 0 {
		t.Fatalf("berth --help exited %d\n%s", exit, stderr)
	}
	commandHelp := map[string]string{}
	helpOf := func(cmd string) string {
		if h, ok := commandHelp[cmd]; ok {
			return h
		}
		out, _, code := runBerthOffline(t, cmd, "--help")
		if code != 0 {
			out = ""
		}
		commandHelp[cmd] = out
		return out
	}
	hasFlag := func(help, flag string) bool {
		return regexp.MustCompile(`(^|[\s\[|,(])` + regexp.QuoteMeta(flag) + `($|[\s\]|,=)])`).MatchString(help)
	}
	word := regexp.MustCompile(`^[a-z][a-z-]*$`)
	flag := regexp.MustCompile(`^--?[a-zA-Z][a-zA-Z0-9-]*`)

	named := 0
	for _, path := range agentGuides {
		for _, call := range berthInvocations(readGuide(t, path)) {
			tokens := strings.Fields(call)
			var cmd string
			for i, tok := range tokens {
				tok = strings.Trim(tok, ".,;:)")
				if tok == "--" {
					break
				}
				if f := flag.FindString(tok); f != "" {
					help := topHelp
					if cmd != "" {
						help = helpOf(cmd)
					}
					if !hasFlag(help, f) {
						t.Errorf("%s: `berth %s` passes %s, which berth %s--help does not list", path, call, f, cmd+" ")
					}
					continue
				}
				if cmd == "" {
					if !word.MatchString(tok) {
						break
					}
					cmd = tok
					named++
					if !regexp.MustCompile(`(?m)^\s+`+regexp.QuoteMeta(cmd)+`\s`).MatchString(topHelp) || helpOf(cmd) == "" {
						t.Errorf("%s: names berth %s, which berth --help does not list", path, cmd)
					}
					continue
				}
				subs := regexp.MustCompile(`berth `+regexp.QuoteMeta(cmd)+` ([a-z][a-z-]*)`).FindAllStringSubmatch(helpOf(cmd), -1)
				if len(subs) > 0 && i > 0 && word.MatchString(tok) && strings.Trim(tokens[i-1], ".,;:)") == cmd {
					known := false
					for _, s := range subs {
						known = known || s[1] == tok
					}
					if !known {
						t.Errorf("%s: names berth %s %s, which berth %s --help does not list", path, cmd, tok, cmd)
					}
				}
			}
		}
	}
	if named == 0 {
		t.Fatal("the guides name no berth commands in code")
	}
}
