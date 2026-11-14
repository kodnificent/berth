package agents

import (
	"os"
	"regexp"
	"testing"
)

func TestClaudeSkillBodyIsTheWorkingGuide(t *testing.T) {
	data, err := os.ReadFile("../../plugins/claude/skills/working-in-a-berth/SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	front := regexp.MustCompile(`\A---\n(?s:.*?)\n---\n\n`).Find(data)
	if front == nil {
		t.Fatalf("SKILL.md does not open with frontmatter between --- lines and a blank line")
	}
	if string(data[len(front):]) != working {
		t.Errorf("SKILL.md differs from working-in-a-berth.md; run go generate ./docs/agents")
	}
}

func TestClaudeSkillNamesItselfAndSaysWhenToUseIt(t *testing.T) {
	data, err := os.ReadFile("../../plugins/claude/skills/working-in-a-berth/SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	front := regexp.MustCompile(`\A---\n(?s:(.*?))\n---\n`).FindSubmatch(data)
	if front == nil {
		t.Fatal("SKILL.md has no frontmatter")
	}
	if !regexp.MustCompile(`(?m)^name: working-in-a-berth$`).Match(front[1]) {
		t.Errorf("the frontmatter does not name the skill working-in-a-berth:\n%s", front[1])
	}
	if !regexp.MustCompile(`(?m)^description: .*\bUse\b`).Match(front[1]) {
		t.Errorf("the frontmatter has no description saying when to use the skill:\n%s", front[1])
	}
}
