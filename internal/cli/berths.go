package cli

import (
	"fmt"
	"io"
	"os"
	"slices"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/kodnificent/berth/internal/apperr"
	"github.com/kodnificent/berth/internal/bytesize"
	"github.com/kodnificent/berth/internal/config"
	"github.com/kodnificent/berth/internal/hook"
	"github.com/kodnificent/berth/internal/hostdb"
	"github.com/kodnificent/berth/internal/ports"
	"github.com/kodnificent/berth/internal/registry"
	"github.com/kodnificent/berth/internal/volume"
	"github.com/kodnificent/berth/internal/worktree"
)

func init() {
	commands = append(commands,
		Command{Name: "add", Summary: "create the berth for a worktree", Usage: "Usage: berth add [--wait] [path]\n\nFlags:\n  --wait     return once every volume clone has finished; exit 4 if one failed\n", Run: runAdd},
		Command{Name: "list", Summary: "list every berth on this machine", Usage: "Usage: berth list\n\nFlags:\n", Run: runList},
		Command{Name: "status", Summary: "show a worktree's berth", Usage: "Usage: berth status [--wait] [path]\n\nFlags:\n  --wait     return once volume clones and post-add have finished; exit 4 if a clone failed, 1 if post-add did\n", Run: runStatus},
		Command{Name: "env", Summary: "print a berth's managed variables for eval", Usage: "Usage: berth env [path]\n\nFlags:\n", Run: runEnv},
	)
}

type berthJSON struct {
	ID         string    `json:"id"`
	Worktree   string    `json:"worktree"`
	Repository string    `json:"repository"`
	Source     string    `json:"source"`
	CreatedAt  time.Time `json:"created_at"`
}

func toJSON(rec registry.Record) berthJSON {
	return berthJSON{
		ID:         rec.ID,
		Worktree:   rec.Worktree,
		Repository: rec.Repository,
		Source:     rec.Repository,
		CreatedAt:  rec.CreatedAt,
	}
}

// pathArg accepts at most one positional worktree path, as git worktree does.
func pathArg(name string, args []string) (string, error) {
	var paths []string
	for _, a := range args {
		if strings.HasPrefix(a, "-") && a != "-" {
			return "", apperr.New(apperr.UnknownFlag, "unknown flag %q for berth %s", a, name)
		}
		paths = append(paths, a)
	}
	if len(paths) > 1 {
		return "", apperr.New(apperr.Usage, "berth %s takes at most one worktree path, got %d", name, len(paths))
	}
	if len(paths) == 1 {
		return paths[0], nil
	}
	return "", nil
}

func resolveBerthWorktree(name string, args []string) (*worktree.Worktree, error) {
	path, err := pathArg(name, args)
	if err != nil {
		return nil, err
	}
	w, err := worktree.Resolve(path)
	if err != nil {
		return nil, err
	}
	if w.IsMain() {
		return nil, apperr.New(apperr.MainWorktree, "%s is the main worktree: it is the source environment, not a berth; run berth %s in a linked worktree", w.Path, name).
			WithNext(mainWorktreeHint(name))
	}
	return w, nil
}

// mainWorktreeHint points add and status at a new worktree, since either can start one
// off. Every other command acts on a berth that already exists, which a new worktree
// would not have.
func mainWorktreeHint(name string) string {
	if name == "add" || name == "status" {
		return "main is the source berths clone from; berths live in linked worktrees, so make one with git worktree add and run berth there"
	}
	return fmt.Sprintf("main is the source berths clone from; berths live in linked worktrees, so run berth %s in the one that holds the berth, and berth list to find it", name)
}

func runAdd(ctx *Context, args []string) error {
	wait := slices.Contains(args, "--wait")
	args = slices.DeleteFunc(slices.Clone(args), func(a string) bool { return a == "--wait" })
	w, err := resolveBerthWorktree("add", args)
	if err != nil {
		return err
	}
	cfg, err := config.Load(w)
	if err != nil {
		return err
	}
	if err := checkEnvWritable(w); err != nil {
		return err
	}
	rec, created, err := addBerth(ctx, w, cfg)
	if err != nil {
		return err
	}
	var failed error
	if wait {
		failed = waitClones(ctx.Stderr, cfg, rec)
		if failed == nil {
			failed = databaseFailed(rec)
		}
		if failed == nil {
			failed = sqliteFailed(rec)
		}
	}
	if ctx.JSON {
		if failed != nil {
			return failed
		}
		return writeJSON(ctx.Stdout, struct {
			berthJSON
			Created bool `json:"created"`
		}{toJSON(rec), created})
	}
	verb := "Added"
	if !created {
		verb = "Already have"
	}
	if _, err := fmt.Fprintf(ctx.Stdout, "%s berth %s for %s\n", verb, rec.ID, rec.Worktree); err != nil {
		return err
	}
	if err := writeRows(ctx.Stdout, summaryRows(viewBerth(cfg, rec).rows(rec))); err != nil {
		return err
	}
	return failed
}

// addBerth holds the registry lock only to find or create the record and hand out its
// names and ports. Everything slower, from cloning files to comparing build inputs,
// runs under the worktree's own lock, so other berth commands never wait for it.
func addBerth(ctx *Context, w *worktree.Worktree, cfg *config.Config) (registry.Record, bool, error) {
	lock, err := registry.LockWorktree(w)
	if err != nil {
		return registry.Record{}, false, err
	}
	defer lock.Close()
	plan, err := planCompose(w, cfg)
	if err != nil {
		return registry.Record{}, false, err
	}
	// Before anything is made: an override compose misreads would collide with main.
	if plan.own != nil {
		if err := checkComposeVersion(); err != nil {
			return registry.Record{}, false, err
		}
	}
	db, err := prepareDatabases(w, cfg)
	if err != nil {
		return registry.Record{}, false, err
	}
	defer db.close()
	rs, err := prepareRedis(w, cfg)
	if err != nil {
		return registry.Record{}, false, err
	}
	defer rs.close()
	rec, created, err := register(w, cfg, plan, db, rs)
	if err != nil {
		return registry.Record{}, false, err
	}
	db.create(ctx.Stderr, &rec)
	err = materialize(ctx.Stderr, w, cfg, plan, &rec)
	if err == nil {
		err = copySQLite(ctx.Stderr, w, cfg, &rec)
	}
	if err == nil {
		shareImages(ctx.Stderr, w, plan, &rec)
		ensureShared(ctx.Stderr, cfg, plan)
		createFresh(ctx.Stderr, cfg, plan, &rec)
		err = startClones(ctx.Stderr, cfg, plan, &rec, db.hostConn())
	}
	if err == nil {
		err = schedulePostAdd(ctx.Stderr, cfg, w, rec)
	}
	if saveErr := saveRecord(rec); err == nil {
		err = saveErr
	}
	return rec, created, err
}

func register(w *worktree.Worktree, cfg *config.Config, plan *composePlan, db *dbSetup, rs *redisSetup) (registry.Record, bool, error) {
	reg, err := registry.Open()
	if err != nil {
		return registry.Record{}, false, err
	}
	defer reg.Close()
	rec, created, err := reg.Add(w, provision(w, cfg, plan, db, rs))
	if err == nil {
		noteUse(rec.ID)
	}
	return rec, created, err
}

func saveRecord(rec registry.Record) error {
	reg, err := registry.Open()
	if err != nil {
		return err
	}
	defer reg.Close()
	return reg.Update(rec)
}

type statusJSON struct {
	berthJSON
	Name           string            `json:"name"`
	Host           string            `json:"host"`
	URL            string            `json:"url,omitempty"`
	ComposeProject string            `json:"compose_project"`
	Ports          []ports.Port      `json:"ports"`
	Env            map[string]string `json:"env"`
	Unresolved     map[string]string `json:"unresolved,omitempty"`
	Files          []string          `json:"files"`
	State          []stateJSON       `json:"state"`
	PostAdd        *postAddJSON      `json:"post_add,omitempty"`
	Shadowed       []shadowJSON      `json:"shadowed"`
	Builds         []registry.Build  `json:"builds"`
	Next           []string          `json:"next"`
	// Error is set when status --wait finds that a clone or post-add failed.
	Error *errorJSON `json:"error,omitempty"`
}

// shadowJSON is a managed key the environment berth runs in exports with another value.
// Compose and most frameworks let an exported variable beat .env, so the berth's value
// doesn't apply in that shell.
type shadowJSON struct {
	Key      string `json:"key"`
	Exported string `json:"exported"`
	Value    string `json:"value"`
}

// shadowed lists the managed keys the environment overrides. secret is the key holding a
// login, whose values are made fit to print.
func shadowed(env map[string]string, secret string) []shadowJSON {
	out := []shadowJSON{}
	for _, k := range sortedKeys(env) {
		if v, ok := os.LookupEnv(k); ok && v != env[k] {
			value := env[k]
			if k == secret {
				v, value = hostdb.Redacted(v), hostdb.Redacted(value)
			}
			out = append(out, shadowJSON{k, v, value})
		}
	}
	return out
}

// findBerth loads the config and the berth of the worktree a read-only command targets,
// and records the command as a use of the berth.
func findBerth(name string, args []string) (*config.Config, registry.Record, error) {
	w, err := resolveBerthWorktree(name, args)
	if err != nil {
		return nil, registry.Record{}, err
	}
	cfg, err := config.Load(w)
	if err != nil {
		return nil, registry.Record{}, err
	}
	reg, err := registry.Open()
	if err != nil {
		return nil, registry.Record{}, err
	}
	defer reg.Close()
	rec, ok, err := reg.Find(w)
	if err != nil {
		return nil, registry.Record{}, err
	}
	if !ok {
		return nil, registry.Record{}, apperr.New(apperr.NoBerth, "worktree %s has no berth; run berth add", w.Path).
			WithNext("run berth add before starting the app or its tests here")
	}
	noteUse(rec.ID)
	return cfg, rec, nil
}

func runStatus(ctx *Context, args []string) error {
	wait := slices.Contains(args, "--wait")
	args = slices.DeleteFunc(slices.Clone(args), func(a string) bool { return a == "--wait" })
	cfg, rec, err := findBerth("status", args)
	if err != nil {
		return err
	}
	if wait {
		if err := waitSettled(ctx, rec.ID); err != nil {
			return err
		}
		if sqliteCopying(rec) {
			if err := registry.WaitWorktree(rec.AdminDir); err != nil {
				return err
			}
			if _, rec, err = findBerth("status", args); err != nil {
				return err
			}
		}
	}
	v := viewBerth(cfg, rec)
	var failed *apperr.Error
	if wait {
		failed = unsettled(v.state, v.postAdd)
	}
	if ctx.JSON {
		body := statusJSON{toJSON(rec), v.eff.Name, v.eff.Host, v.eff.URL, rec.ComposeProject, v.ports, v.eff.Env, v.eff.Unresolved, v.files, v.state, v.postAdd, v.shadow, v.builds, v.next, nil}
		if failed != nil {
			body.Error = &errorJSON{failed.Code, failed.Message, failed.Next}
		}
		if err := writeJSON(ctx.Stdout, body); err != nil {
			return err
		}
		if failed != nil {
			return &apperr.Reported{Code: failed.Code}
		}
		return nil
	}
	if err := writeRows(ctx.Stdout, v.rows(rec)); err != nil {
		return err
	}
	if failed != nil {
		return failed
	}
	return nil
}

// berthView is what status reports about a berth, as it is now.
type berthView struct {
	eff     config.Effective
	ports   []ports.Port
	files   []string
	state   []stateJSON
	postAdd *postAddJSON
	shadow  []shadowJSON
	builds  []registry.Build
	next    []string
}

func viewBerth(cfg *config.Config, rec registry.Record) berthView {
	assigned := map[string]int{}
	for _, p := range rec.Ports {
		if p.HostPort > 0 {
			assigned[p.Name] = p.HostPort
		}
	}
	v := berthView{eff: cfg.Resolve(rec.Name, cfg.PortValues(assigned))}
	v.eff.Env = managedEnv(cfg, rec)
	v.ports = ephemeralPorts(rec)
	if len(v.ports) == 0 {
		v.ports = []ports.Port{}
	}
	v.files = []string{}
	if len(v.eff.Env) > 0 {
		v.files = []string{".env"}
	}
	v.files = union(v.files, rec.Files)
	if v.files == nil {
		v.files = []string{}
	}
	v.state = append(stateEntries(rec), sqliteEntries(rec)...)
	v.postAdd = postAddEntry(rec.ID)
	v.shadow = shadowed(v.eff.Env, secretEnvKey(rec))
	v.eff.Env = redactEnv(rec, v.eff.Env)
	v.builds = rec.Builds
	if v.builds == nil {
		v.builds = []registry.Build{}
	}
	v.next = statusHints(v.eff.URL, v.ports, v.state, v.postAdd, os.Getenv("BERTH_PATH") != "")
	return v
}

// rows are the lines status prints, in order.
func (v berthView) rows(rec registry.Record) []row {
	eff, recPorts, files, state, builds, postAdd, shadow, next := v.eff, v.ports, v.files, v.state, v.builds, v.postAdd, v.shadow, v.next
	var out []row
	add := func(kind rowKind, label, value string) {
		out = append(out, row{kind: kind, label: label, value: value})
	}
	add(rowOther, "berth", rec.ID)
	add(rowName, "name", rec.Name)
	add(rowOther, "worktree", rec.Worktree)
	add(rowOther, "repository", rec.Repository)
	add(rowHost, "host", eff.Host)
	if eff.URL != "" {
		add(rowURL, "url", eff.URL)
	}
	if rec.ComposeProject != "" {
		add(rowOther, "compose project", rec.ComposeProject)
	}
	for _, p := range recPorts {
		kind, host := rowOther, p.Policy
		if p.HostPort > 0 {
			host = fmt.Sprintf("%d (%s)", p.HostPort, p.Policy)
			if p.Policy == ports.Assign {
				kind = rowAssignedPort
			}
		}
		add(kind, "port "+p.Name, host)
	}
	for _, k := range sortedKeys(eff.Env) {
		add(rowOther, "env "+k, eff.Env[k])
	}
	for _, k := range sortedKeys(eff.Unresolved) {
		add(rowOther, "unresolved "+k, eff.Unresolved[k])
	}
	for _, sh := range shadow {
		add(rowOther, "shadowed "+sh.Key, fmt.Sprintf("exported as %s in this shell, which overrides the berth's %s", sh.Exported, sh.Value))
	}
	add(rowOther, "files", strings.Join(files, ", "))
	for _, s := range state {
		line := s.Status
		if s.Status == "ready" {
			line = fmt.Sprintf("ready (%s, %s", s.Strategy, bytesize.Format(s.Bytes))
			if s.PauseSeconds != nil {
				line += fmt.Sprintf(", main's %s paused %.1fs", strings.Join(s.Paused, ", "), *s.PauseSeconds)
			}
			line += ")"
		} else if s.Progress != nil {
			line = fmt.Sprintf("cloning (%d%%, %s of %s)", int(*s.Progress*100), bytesize.Format(s.CopiedBytes), bytesize.Format(s.TotalBytes))
		} else if s.Error != "" {
			line += ": " + s.Error
		}
		name, m := s.Name, stateMark(s.Status)
		if s.Kind == kindSQLite && s.SourceMissing {
			line, m = "none: main has no such file", markNone
		}
		if s.Kind == kindDatabase {
			name = s.Target
			if s.Status == volume.Ready {
				switch s.Strategy {
				case config.None:
					line = "ready (none: main's own database, shared with main)"
				case config.Dump:
					line = fmt.Sprintf("ready (dump, %s, from %s", bytesize.Format(s.Bytes), s.Source)
					if s.PauseSeconds != nil {
						line += fmt.Sprintf("; writes to main's %s paused %.2fs", strings.Join(s.Paused, ", "), *s.PauseSeconds)
					}
					line += ")"
				case config.File:
					line = fmt.Sprintf("ready (seed file, %s, for %s)", bytesize.Format(s.Bytes), s.Source)
				case config.Command:
					line = fmt.Sprintf("ready (command, for %s)", s.Source)
				case config.Template:
					line = fmt.Sprintf("ready (template, %s, %s, from %s)", s.Method, bytesize.Format(s.Bytes), s.Source)
				case config.Snapshot:
					line = fmt.Sprintf("ready (snapshot, %s, from %s", bytesize.Format(s.Bytes), s.Source)
					if s.SnapshotTakenAt != nil {
						line += ", taken " + ago(time.Since(*s.SnapshotTakenAt))
					}
					line += ")"
				default:
					line = fmt.Sprintf("ready (%s, from %s)", s.Strategy, s.Source)
				}
			}
		}
		out = append(out, row{rowState, s.Kind + " " + name, line, m})
		for _, n := range s.Notes {
			add(rowOther, s.Kind+" "+name, "note: "+n)
		}
		if s.Log != "" {
			add(rowOther, s.Kind+" "+name, "log: "+s.Log)
		}
	}
	for _, b := range builds {
		line := "main's image"
		if !b.Shared {
			line = "builds on up: " + b.Reason
		}
		add(rowOther, "build "+b.Service, line)
	}
	if postAdd != nil {
		out = append(out, postAddRows(postAdd)...)
	}
	for _, h := range next {
		add(rowNext, "next", h)
	}
	return out
}

// statusHints says what to do next with a berth: wait while it settles, then use its
// own URL or ports. Inside a hook berth runs, berth holds the locks --wait would wait
// on, and a running post-add is the hook itself, so neither is worth waiting for there.
func statusHints(url string, recPorts []ports.Port, state []stateJSON, postAdd *postAddJSON, inHook bool) []string {
	next := []string{}
	var busy []string
	settled := true
	for _, s := range state {
		switch s.Status {
		case volume.Cloning:
			busy = append(busy, s.Kind+" "+s.Name+" is still cloning")
		case volume.Failed:
			settled = false
		}
	}
	if postAdd != nil {
		switch postAdd.Status {
		case hook.Pending, hook.Running:
			if !inHook {
				busy = append(busy, "post-add is still running")
			}
		case hook.OK:
		default:
			settled = false
		}
	}
	if len(busy) > 0 {
		h := strings.Join(busy, " and ")
		if inHook {
			h += ", so its data is not complete yet"
		} else {
			h += "; run berth status --wait before starting the app"
		}
		return append(next, h)
	}
	if !settled {
		return next
	}
	if url != "" {
		return append(next, fmt.Sprintf("use %s to reach this berth's app, not localhost or main's URL", url))
	}
	for _, p := range recPorts {
		if p.HostPort > 0 {
			return append(next, "use the ports listed here to reach this berth's services, not main's")
		}
	}
	return next
}

// waitSettled returns once berth id has no clone and no post-add waiting or running.
// The clone lock goes first: post-add holds its own lock from before its clones end.
// A berth hook runs while berth holds those locks for it, so from inside one there is
// nothing to wait for that could ever end.
func waitSettled(ctx *Context, id string) error {
	if os.Getenv("BERTH_PATH") != "" {
		return nil
	}
	cloneLock, cloneFree, err := volume.TryLock(id)
	if err != nil {
		return err
	}
	postLock, postFree, err := hook.TryLock(id)
	if err != nil {
		if cloneLock != nil {
			cloneLock.Close()
		}
		return err
	}
	for _, l := range []*os.File{cloneLock, postLock} {
		if l != nil {
			l.Close()
		}
	}
	if cloneFree && postFree {
		return nil
	}
	if !ctx.JSON {
		fmt.Fprintln(ctx.Stderr, "berth: waiting for the berth's clones and post-add to finish")
	}
	if err := volume.WaitUnlocked(id); err != nil {
		return err
	}
	return hook.WaitUnlocked(id)
}

// unsettled is what status --wait fails with: exit 4 when a clone failed, as add --wait
// does, and 1 when post-add failed or was skipped.
func unsettled(state []stateJSON, postAdd *postAddJSON) *apperr.Error {
	var failed []string
	for _, s := range state {
		if s.Status != volume.Ready {
			name := s.Name
			if s.Kind == kindDatabase {
				name = s.Target
			}
			failed = append(failed, fmt.Sprintf("%s %s: %s", s.Kind, name, s.Error))
		}
	}
	if len(failed) > 0 {
		return apperr.New(apperr.StateCloneFailed, "cloning failed for %s", strings.Join(failed, "; "))
	}
	if postAdd != nil && postAdd.Status != hook.OK {
		msg := "post-add " + postAdd.Status
		if postAdd.ExitCode != nil {
			msg += fmt.Sprintf(" (exit %d)", *postAdd.ExitCode)
		}
		if postAdd.Error != "" {
			msg += ": " + postAdd.Error
		}
		return apperr.New(apperr.Generic, "%s", msg)
	}
	return nil
}

// runEnv prints export lines a POSIX shell can eval, for tools that ignore .env.
func runEnv(ctx *Context, args []string) error {
	cfg, rec, err := findBerth("env", args)
	if err != nil {
		return err
	}
	env := managedEnv(cfg, rec)
	if ctx.JSON {
		return writeJSON(ctx.Stdout, map[string]any{"env": redactEnv(rec, env)})
	}
	// Unlike --json, these lines keep a URL's login: eval must give the shell what the
	// app reads from .env.
	var b strings.Builder
	for _, k := range sortedKeys(env) {
		fmt.Fprintf(&b, "export %s='%s'\n", k, strings.ReplaceAll(env[k], "'", `'\''`))
	}
	_, err = io.WriteString(ctx.Stdout, b.String())
	return err
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func runList(ctx *Context, args []string) error {
	for _, a := range args {
		if strings.HasPrefix(a, "-") {
			return apperr.New(apperr.UnknownFlag, "unknown flag %q for berth list", a)
		}
		return apperr.New(apperr.Usage, "berth list takes no arguments, got %q", a)
	}
	reg, err := registry.Open()
	if err != nil {
		return err
	}
	defer reg.Close()
	all, err := reg.All()
	if err != nil {
		return err
	}
	resumeDeadClones(all, ctx.Stderr)
	if ctx.JSON {
		type entry struct {
			berthJSON
			Name      string       `json:"name"`
			Orphaned  bool         `json:"orphaned"`
			Locked    bool         `json:"locked"`
			DiskBytes int64        `json:"disk_bytes"`
			Host      string       `json:"host"`
			Ports     []ports.Port `json:"ports"`
			LastUsed  time.Time    `json:"last_used"`
			InUse     *bool        `json:"in_use"`
		}
		disk := diskUse(all)
		used := usage(all, ctx.Stderr)
		list := make([]entry, 0, len(all))
		for _, b := range all {
			held := b.Ports
			if held == nil {
				held = []ports.Port{}
			}
			u := used[b.ID]
			list = append(list, entry{toJSON(b.Record), b.Name, b.Orphaned, b.Locked, disk[b.ID], b.Host, held, u.lastUsed, u.inUse})
		}
		return writeJSON(ctx.Stdout, map[string]any{"berths": list})
	}
	if len(all) == 0 {
		_, err := fmt.Fprintln(ctx.Stdout, "No berths.")
		return err
	}
	tw := tabwriter.NewWriter(ctx.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "BERTH\tNAME\tHOST\tSTATE\tUSED\tDISK\tWORKTREE\tREPOSITORY")
	disk := diskUse(all)
	used := usage(all, ctx.Stderr)
	now := time.Now()
	for _, b := range all {
		state := "ok"
		switch {
		case b.Orphaned:
			state = "orphaned"
		case b.Locked:
			state = "locked"
		}
		u := used[b.ID]
		when := ago(now.Sub(u.lastUsed))
		if u.inUse != nil && *u.inUse {
			when = "in use"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", b.ID, b.Name, b.Host, state, when, bytesize.Format(disk[b.ID]), b.Worktree, b.Repository)
	}
	return tw.Flush()
}

func ago(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d/time.Minute))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d/time.Hour))
	}
	return fmt.Sprintf("%dd ago", int(d/(24*time.Hour)))
}
