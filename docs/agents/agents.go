// Package agents carries the agent guides into the binary. It lives beside the guides
// because go:embed cannot reach a parent directory.
package agents

import _ "embed"

//go:generate sh ../../scripts/sync-claude-skill.sh

//go:embed working-in-a-berth.md
var working string

//go:embed setting-up-berth.md
var settingUp string

type Guide struct {
	Name string
	Text string
}

func Guides() []Guide {
	return []Guide{
		{Name: "working-in-a-berth", Text: working},
		{Name: "setting-up-berth", Text: settingUp},
	}
}
