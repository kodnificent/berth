package compose

import (
	"bytes"
	"path/filepath"
	"testing"
)

const builds = `name: app
services:
  web:
    build:
      context: .
      dockerfile: docker/Dockerfile
      target: dev
      args:
        MODE: dev
  worker:
    build: ./worker
    image: registry.example/app:dev
  queue:
    image: registry.example/app:dev
  db:
    image: mysql:8
`

func TestBuildsAreComparableAcrossWorktrees(t *testing.T) {
	var got [2][]Build
	var roots [2]string
	for i := range got {
		roots[i] = t.TempDir()
		write(t, filepath.Join(roots[i], "compose.yml"), builds)
		p, err := Load(roots[i], Options{})
		if err != nil {
			t.Fatal(err)
		}
		got[i] = p.Builds(roots[i])
	}
	if len(got[0]) != 2 {
		t.Fatalf("Builds = %+v, want web and worker", got[0])
	}
	web, worker := got[0][0], got[0][1]
	if web.Service != "web" || web.Image != "app-web" || web.Target != "dev" || web.Dockerfile != filepath.Join(roots[0], "docker", "Dockerfile") || web.Context != roots[0] {
		t.Errorf("web build = %+v", web)
	}
	if worker.Service != "worker" || worker.Image != "registry.example/app:dev" || worker.Context != filepath.Join(roots[0], "worker") {
		t.Errorf("worker build = %+v", worker)
	}
	for i := range got[0] {
		if !bytes.Equal(got[0][i].Spec, got[1][i].Spec) {
			t.Errorf("%s spec differs between worktrees:\n%s\n%s", got[0][i].Service, got[0][i].Spec, got[1][i].Spec)
		}
		if bytes.Contains(got[0][i].Spec, []byte(roots[0])) {
			t.Errorf("%s spec still names the worktree: %s", got[0][i].Service, got[0][i].Spec)
		}
	}
}

func TestOverrideRenamesBuiltImagesItNames(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "compose.yml"), builds)
	p, err := Load(dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(dir, "compose.berth.yml"), string(p.Override(HostSide{Project: "app-feat"})))
	t.Setenv("COMPOSE_PROJECT_NAME", "app-feat")
	b, err := Load(dir, Options{Files: []string{"compose.yml", "compose.berth.yml"}})
	if err != nil {
		t.Fatal(err)
	}
	for svc, want := range map[string]string{"web": "", "worker": "app-feat-worker", "queue": "app-feat-worker", "db": "mysql:8"} {
		if got := b.model.Services[svc].Image; got != want {
			t.Errorf("%s image = %q, want %q", svc, got, want)
		}
	}
	if got := b.Builds(dir)[1].Image; got != "app-feat-worker" {
		t.Errorf("worker builds %q in the berth, want app-feat-worker", got)
	}
}
