// Package dockerfile works out which parts of a build context a Dockerfile target reads,
// so two contexts can be compared on what a build actually uses.
package dockerfile

import (
	"bytes"
	"encoding/csv"
	"path"
	"strconv"
	"strings"

	"github.com/moby/buildkit/frontend/dockerfile/parser"
)

// Reads is what building one target of a Dockerfile reads beyond the Dockerfile itself.
type Reads struct {
	// Whole is true when the build may read anything in the context.
	Whole bool
	// Paths are context-relative paths or globs, in slash form, that the build copies or
	// mounts. A directory stands for everything under it.
	Paths []string
	// Contexts are named build contexts the build reads from.
	Contexts []string
}

type stage struct {
	name string
	base string
	// deps are stages or named contexts this stage reads through --from.
	deps  []string
	paths []string
	// unknownDep is true when a --from or FROM can't be resolved without build args.
	unknownDep bool
	whole      bool
}

// Analyze reports what building target reads; target "" is the last stage. When it
// can't tell, it says the whole context, which is never wrong, only cautious.
func Analyze(content []byte, target string) Reads {
	if syntax, _, _, ok := parser.DetectSyntax(content); ok && !isStockFrontend(syntax) {
		// Another frontend may read the context in any way it likes.
		return Reads{Whole: true}
	}
	res, err := parser.Parse(bytes.NewReader(content))
	if err != nil {
		return Reads{Whole: true}
	}
	var stages []*stage
	for _, n := range res.AST.Children {
		cmd := strings.ToLower(n.Value)
		if cmd == "from" {
			args := words(n)
			s := &stage{}
			if len(args) > 0 {
				s.base = args[0]
			}
			if len(args) >= 3 && strings.EqualFold(args[1], "as") {
				s.name = strings.ToLower(args[2])
			}
			stages = append(stages, s)
			continue
		}
		if len(stages) == 0 {
			continue
		}
		s := stages[len(stages)-1]
		switch cmd {
		case "copy", "add":
			args := words(n)
			if len(args) < 2 {
				continue
			}
			if from := flag(n.Flags, "from"); from != "" {
				s.addDep(from)
				continue
			}
			for _, src := range args[:len(args)-1] {
				switch {
				case strings.HasPrefix(src, "<<"):
				case cmd == "add" && isRemote(src):
				case strings.Contains(src, "$"):
					// A build arg could make it any path.
					s.whole = true
				default:
					s.paths = append(s.paths, src)
				}
			}
		case "run":
			for _, f := range n.Flags {
				v, ok := strings.CutPrefix(f, "--mount=")
				if !ok {
					continue
				}
				m, ok := mount(v)
				if !ok {
					s.whole = true
					continue
				}
				if m["type"] != "" && m["type"] != "bind" {
					continue
				}
				if m["from"] != "" {
					s.addDep(m["from"])
					continue
				}
				src := m["source"]
				if src == "" {
					src = m["src"]
				}
				switch {
				case src == "":
					s.whole = true
				case strings.Contains(src, "$"):
					s.whole = true
				default:
					s.paths = append(s.paths, src)
				}
			}
		}
	}
	if len(stages) == 0 {
		return Reads{Whole: true}
	}

	byName := map[string]int{}
	for i, s := range stages {
		if s.name != "" {
			byName[s.name] = i
		}
		if strings.Contains(s.base, "$") {
			s.unknownDep = true
		}
	}
	start := len(stages) - 1
	if target != "" {
		i, ok := byName[strings.ToLower(target)]
		if !ok {
			return Reads{Whole: true}
		}
		start = i
	}

	var out Reads
	seenStage := map[int]bool{}
	seenContext := map[string]bool{}
	var visit func(i int)
	visit = func(i int) {
		if seenStage[i] {
			return
		}
		seenStage[i] = true
		s := stages[i]
		if s.whole || s.unknownDep {
			out.Whole = true
		}
		out.Paths = append(out.Paths, s.paths...)
		for _, ref := range append([]string{s.base}, s.deps...) {
			if j, ok := stageIndex(ref, byName, i); ok {
				visit(j)
			} else if !seenContext[ref] {
				// Most of these are images; the caller keeps those that name a context.
				seenContext[ref] = true
				out.Contexts = append(out.Contexts, ref)
			}
		}
	}
	visit(start)
	if out.Whole {
		return Reads{Whole: true}
	}
	return out
}

func (s *stage) addDep(ref string) {
	if strings.Contains(ref, "$") {
		s.unknownDep = true
		return
	}
	s.deps = append(s.deps, ref)
}

// stageIndex finds the earlier stage ref names, by name or by index.
func stageIndex(ref string, byName map[string]int, before int) (int, bool) {
	if i, ok := byName[strings.ToLower(ref)]; ok && i < before {
		return i, true
	}
	if i, err := strconv.Atoi(ref); err == nil && i >= 0 && i < before {
		return i, true
	}
	return 0, false
}

func words(n *parser.Node) []string {
	var out []string
	for w := n.Next; w != nil; w = w.Next {
		out = append(out, w.Value)
	}
	return out
}

func flag(flags []string, name string) string {
	for _, f := range flags {
		if v, ok := strings.CutPrefix(f, "--"+name+"="); ok {
			return v
		}
	}
	return ""
}

// mount reads a --mount value, which is CSV of key=value fields.
func mount(v string) (map[string]string, bool) {
	fields, err := csv.NewReader(strings.NewReader(v)).Read()
	if err != nil {
		return nil, false
	}
	out := map[string]string{}
	for _, f := range fields {
		k, val, _ := strings.Cut(f, "=")
		out[strings.ToLower(k)] = val
	}
	return out, true
}

func isRemote(src string) bool {
	return strings.Contains(src, "://") || strings.HasPrefix(src, "git@")
}

func isStockFrontend(syntax string) bool {
	ref := strings.TrimPrefix(syntax, "docker.io/")
	return strings.HasPrefix(ref, "docker/dockerfile:") || strings.HasPrefix(ref, "docker/dockerfile@") || ref == "docker/dockerfile"
}

// Clean makes a Dockerfile source path context-relative in slash form: "." for the
// whole context.
func Clean(p string) string {
	if c := strings.TrimPrefix(path.Clean("/"+p), "/"); c != "" {
		return c
	}
	return "."
}
