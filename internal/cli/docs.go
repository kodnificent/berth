package cli

import (
	"io"

	"github.com/kodnificent/berth/docs/agents"
	"github.com/kodnificent/berth/internal/apperr"
)

const docsUsage = "Usage: berth docs agents\n\nberth docs agents prints the agent guides built into this binary: working in a\nberth, then setting berth up in a repository.\n\nFlags:\n"

func init() {
	commands = append(commands, Command{Name: "docs", Summary: "print the agent guides built into berth", Usage: docsUsage, Run: runDocs})
}

func runDocs(ctx *Context, args []string) error {
	if len(args) != 1 || args[0] != "agents" {
		return apperr.New(apperr.Usage, "usage: berth docs agents")
	}
	guides := agents.Guides()
	if ctx.JSON {
		type guideJSON struct {
			Name     string `json:"name"`
			Markdown string `json:"markdown"`
		}
		list := make([]guideJSON, 0, len(guides))
		for _, g := range guides {
			list = append(list, guideJSON{g.Name, g.Text})
		}
		return writeJSON(ctx.Stdout, map[string]any{"guides": list})
	}
	for i, g := range guides {
		if i > 0 {
			if _, err := io.WriteString(ctx.Stdout, "\n"); err != nil {
				return err
			}
		}
		if _, err := io.WriteString(ctx.Stdout, g.Text); err != nil {
			return err
		}
	}
	return nil
}
