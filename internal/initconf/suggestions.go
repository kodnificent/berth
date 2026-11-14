package initconf

import (
	"fmt"
	"strings"
)

type Preset struct {
	Name  string
	About string
	Env   []PresetEnv
	Ports []PresetPort
	MySQL *PresetMySQL
}

// PresetEnv is an [env.set] key. Reason says why it is commented, when it isn't Active.
type PresetEnv struct {
	Key, Value, Reason string
	Active             bool
}

type PresetPort struct {
	Name, Env, Reason string
	Main              int
	Active            bool
}

type PresetMySQL struct {
	Active                bool
	Reason, From, Default string
	Prefix, PrefixReason  string
	PrefixActive          bool
	Databases             map[string]string
}

func (p Preset) heading() string {
	return fmt.Sprintf("# From the %s preset, for %s.\n", p.Name, p.About)
}

func presetEnvKeys(presets []Preset) map[string]bool {
	keys := map[string]bool{}
	for _, p := range presets {
		for _, e := range p.Env {
			keys[e.Key] = true
		}
		for _, pt := range p.Ports {
			keys[pt.Env] = true
		}
		if p.MySQL != nil {
			if k, ok := strings.CutPrefix(p.MySQL.Prefix, "env:"); ok {
				keys[k] = true
			}
		}
	}
	return keys
}

// writePresetEnv returns one block of [env.set] lines per preset with env suggestions.
// detected holds the template detection found for a key: it comes from the value main
// actually uses, so it beats the preset's generic one.
func writePresetEnv(presets []Preset, detected map[string]string) []string {
	var blocks []string
	for _, p := range presets {
		if len(p.Env) == 0 {
			continue
		}
		var b strings.Builder
		b.WriteString(p.heading())
		for _, e := range p.Env {
			value := e.Value
			if d, ok := detected[e.Key]; ok {
				value = d
			}
			if e.Active {
				fmt.Fprintf(&b, "%s = %s\n", e.Key, quote(value))
			} else {
				fmt.Fprintf(&b, "# %s\n# %s = %s\n", e.Reason, e.Key, quote(value))
			}
		}
		blocks = append(blocks, b.String())
	}
	return blocks
}

func writePresetPorts(b *strings.Builder, presets []Preset) {
	var blocks []string
	for _, p := range presets {
		if len(p.Ports) == 0 {
			continue
		}
		var s strings.Builder
		s.WriteString(p.heading())
		for _, pt := range p.Ports {
			line := fmt.Sprintf("%s = { env = %s }\n", key(pt.Name), quote(pt.Env))
			if pt.Active {
				s.WriteString(line)
			} else {
				fmt.Fprintf(&s, "# %s\n# %s", pt.Reason, line)
			}
		}
		blocks = append(blocks, s.String())
	}
	if len(blocks) > 0 {
		b.WriteString("\n[ports]\n")
		b.WriteString(strings.Join(blocks, "\n"))
	}
}

func writePresetMySQL(b *strings.Builder, presets []Preset) {
	for _, p := range presets {
		if p.MySQL == nil {
			continue
		}
		m := p.MySQL
		var s strings.Builder
		s.WriteString("[state.mysql]\n")
		fmt.Fprintf(&s, "from = %s\n", quote(m.From))
		if m.PrefixActive {
			fmt.Fprintf(&s, "prefix = %s\n", quote(m.Prefix))
		} else {
			fmt.Fprintf(&s, "# %s\n# prefix = %s\n", m.PrefixReason, quote(m.Prefix))
		}
		fmt.Fprintf(&s, "default = %s\n", quote(m.Default))
		if len(m.Databases) > 0 {
			s.WriteString("\n[state.mysql.databases]\n")
			for _, k := range sortedKeys(m.Databases) {
				fmt.Fprintf(&s, "%s = %s\n", key(k), quote(m.Databases[k]))
			}
		}

		b.WriteString("\n")
		b.WriteString(p.heading())
		text := s.String()
		if !m.Active {
			fmt.Fprintf(b, "# %s\n", m.Reason)
			text = commentOut(text)
		}
		b.WriteString(text)
	}
}

// commentOut comments every line of text once, so a line already commented, such as an
// unconfirmed prefix, stays commented when the table is uncommented.
func commentOut(text string) string {
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	for i, l := range lines {
		if l != "" {
			lines[i] = "# " + l
		}
	}
	return strings.Join(lines, "\n") + "\n"
}
