package cli

import (
	"os"
	"path/filepath"

	"github.com/kodnificent/berth/internal/apperr"
	"github.com/kodnificent/berth/internal/compose"
	"github.com/kodnificent/berth/internal/config"
	"github.com/kodnificent/berth/internal/docker"
	"github.com/kodnificent/berth/internal/names"
	"github.com/kodnificent/berth/internal/ports"
	"github.com/kodnificent/berth/internal/registry"
	"github.com/kodnificent/berth/internal/worktree"
)

// provision gives a berth its name, host, compose project, ports, database names and Redis prefix. It
// runs under the registry lock, so others is every other berth on the machine and stays
// so until the record is saved.
func provision(w *worktree.Worktree, cfg *config.Config, plan *composePlan, db *dbSetup, rs *redisSetup) func(*registry.Record, []registry.Record) error {
	return func(rec *registry.Record, others []registry.Record) error {
		main, own := plan.main, plan.own
		mainProject := filepath.Base(w.Repository)
		if main != nil {
			mainProject = main.Name
		}

		usedName, usedHost, usedProject := map[string]string{}, map[string]string{"localhost": "the main worktree"}, map[string]string{mainProject: "the main worktree"}
		reserved := map[int]bool{}
		for _, o := range others {
			usedName[o.Name], usedHost[o.Host], usedProject[o.ComposeProject] = o.Worktree, o.Worktree, o.Worktree
			for _, p := range o.Ports {
				reserved[p.HostPort] = true
			}
		}
		repos := map[string]bool{}
		for _, o := range others {
			if o.Repository != w.Repository {
				repos[o.Repository] = true
			}
		}
		published := []*compose.Project{main}
		for repo := range repos {
			// Another repository's broken or missing compose file is its own problem and
			// must not stop this add; its main simply can't be checked.
			if p, err := compose.Load(repo, compose.Options{ComposeFileEnv: true}); err == nil {
				published = append(published, p)
			}
		}
		for _, project := range published {
			if project == nil {
				continue
			}
			for _, p := range project.Ports {
				for _, hp := range p.HostPorts() {
					reserved[hp] = true
				}
			}
		}

		if rec.Name == "" {
			// A project Docker already has resources for is someone else's, and removing
			// the berth later would take them with it. Unknown when Docker is unreachable.
			inUse, _ := docker.ProjectsInUse()
			base := w.Branch
			if base == "" {
				base = filepath.Base(w.Path)
			}
			names.Candidates(base, cfg.NameMax(), func(n string) bool {
				_, nameTaken := usedName[n]
				_, hostTaken := usedHost[cfg.Resolve(n, nil).Host]
				_, projectTaken := usedProject[names.Project(mainProject, n)]
				projectTaken = projectTaken || inUse[names.Project(mainProject, n)]
				if nameTaken || hostTaken || projectTaken {
					return true
				}
				rec.Name, rec.ComposeProject = n, names.Project(mainProject, n)
				return false
			})
		}
		rec.Host = cfg.Resolve(rec.Name, nil).Host
		if !names.ValidHost(rec.Host) {
			return apperr.New(apperr.ConfigInvalid, "[berth] host gives berth %s the host %q, which is not a valid hostname; shorten the template", rec.Name, rec.Host)
		}
		if other, taken := usedHost[rec.Host]; taken {
			return apperr.New(apperr.Generic, "[berth] host gives this berth the host %s, which %s already uses; put {name} in its own label", rec.Host, other)
		}

		lo, hi := cfg.PoolRange()
		var err error
		rec.Ports, err = ports.Allocate(ports.Wanted(cfg, own), rec.Ports, reserved, lo, hi, ports.Free)
		if err != nil {
			return err
		}
		if err := db.plan(rec, others); err != nil {
			return err
		}
		return rs.plan(rec, others)
	}
}

// envFiles is the worktree's own .env, or the main worktree's until the berth has one,
// so container ports read from variables match what the berth will see.
func envFiles(w *worktree.Worktree) []string {
	if _, err := os.Stat(filepath.Join(w.Path, ".env")); err == nil {
		return nil
	}
	if p := filepath.Join(w.Repository, ".env"); fileExists(p) {
		return []string{p}
	}
	return nil
}

func fileExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.Mode().IsRegular()
}
