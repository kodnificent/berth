package compose

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadReadsPortsFromEveryProfile(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "compose.yml"), `name: app
services:
  web:
    image: busybox
    ports: ["${WEB_PORT:-8000}:80", "9000-9001:9000-9001"]
  test-db:
    image: busybox
    profiles: [test]
    ports: ["3307:3306"]
  worker:
    image: busybox
    env_file: [.env.missing]
    ports: ["5000"]
`)
	write(t, filepath.Join(dir, ".env"), "WEB_PORT=8100\n")
	p, err := Load(dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	var got [][]int
	for _, port := range p.Ports {
		got = append(got, append([]int{port.Target}, port.HostPorts()...))
	}
	want := [][]int{{3306, 3307}, {80, 8100}, {9000, 9000}, {9001, 9001}, {5000}}
	if p.Name != "app" || !reflect.DeepEqual(got, want) {
		t.Errorf("Load = %s %v, want app %v", p.Name, got, want)
	}

	other := filepath.Join(t.TempDir(), "env")
	write(t, other, "WEB_PORT=8200\n")
	if p, err := Load(dir, Options{EnvFiles: []string{other}}); err != nil || p.Ports[1].Published != "8200" {
		t.Errorf("Load with another env file: %v %+v, want web published on 8200", err, p)
	}
}

func TestLoadIgnoresParentDirectories(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "compose.yml"), "services:\n  web:\n    image: busybox\n")
	if p, err := Load(filepath.Join(dir, "nested"), Options{}); err != nil || p != nil {
		t.Errorf("Load in a directory with no compose file = %+v, %v; want nil", p, err)
	}
}

const subfolderCompose = "services:\n  web:\n    image: busybox\n    ports:\n      - \"${WEB_PORT:-1}:80\"\n    volumes:\n      - ./data:/data\n"

func checkSubfolderProject(t *testing.T, p *Project, err error, root string) {
	t.Helper()
	if err != nil || p == nil {
		t.Fatalf("Load = %+v, %v", p, err)
	}
	if p.Name != "docker" {
		t.Errorf("project is named %q, want docker, after the first file's folder as docker compose names it", p.Name)
	}
	want := filepath.Join(root, "docker", "data")
	if m := p.Mounts(); len(m) != 1 || m[0].Source != want {
		t.Errorf("mounts = %+v, want ./data resolved against the first file's folder, %s", m, want)
	}
	if len(p.Ports) != 1 || p.Ports[0].Published != "8123" {
		t.Errorf("ports = %+v, want 8123 read from the .env beside berth's working directory", p.Ports)
	}
}

func TestLoadNamesAProjectAfterItsFirstFilesFolder(t *testing.T) {
	root := filepath.Join(t.TempDir(), "shop")
	write(t, filepath.Join(root, "docker", "compose.yml"), subfolderCompose)
	write(t, filepath.Join(root, "docker", ".env"), "WEB_PORT=9999\n")

	t.Run("COMPOSE_FILE", func(t *testing.T) {
		write(t, filepath.Join(root, ".env"), "COMPOSE_FILE=docker/compose.yml\nWEB_PORT=8123\n")
		p, err := Load(root, Options{ComposeFileEnv: true})
		checkSubfolderProject(t, p, err, root)
	})
	t.Run("Files", func(t *testing.T) {
		write(t, filepath.Join(root, ".env"), "WEB_PORT=8123\n")
		p, err := Load(root, Options{Files: []string{"docker/compose.yml"}})
		checkSubfolderProject(t, p, err, root)
	})
	write(t, filepath.Join(root, "extra.yml"), "services:\n  web:\n    environment:\n      EXTRA: \"1\"\n")
	t.Run("COMPOSE_FILE with a later file in the root", func(t *testing.T) {
		write(t, filepath.Join(root, ".env"), "COMPOSE_FILE=docker/compose.yml"+string(os.PathListSeparator)+"extra.yml\nWEB_PORT=8123\n")
		p, err := Load(root, Options{ComposeFileEnv: true})
		checkSubfolderProject(t, p, err, root)
	})
	t.Run("Files with a later file in the root", func(t *testing.T) {
		write(t, filepath.Join(root, ".env"), "WEB_PORT=8123\n")
		p, err := Load(root, Options{Files: []string{"docker/compose.yml", "extra.yml"}})
		checkSubfolderProject(t, p, err, root)
	})
}

func TestLoadKeepsAProjectsOwnName(t *testing.T) {
	root := filepath.Join(t.TempDir(), "shop")
	write(t, filepath.Join(root, "docker", "compose.yml"), "name: mine\n"+subfolderCompose)
	p, err := Load(root, Options{Files: []string{"docker/compose.yml"}})
	if err != nil || p == nil || p.Name != "mine" {
		t.Fatalf("Load = %+v, %v; want the project named mine", p, err)
	}
}

func TestLoadResolvesAFirstFileReadFromElsewhereInItsOwnPlace(t *testing.T) {
	base := t.TempDir()
	main, wt := filepath.Join(base, "shop"), filepath.Join(base, "wt")
	write(t, filepath.Join(main, "docker", "compose.yml"), subfolderCompose)
	write(t, filepath.Join(wt, ".env"), "WEB_PORT=8123\n")
	p, err := Load(wt, Options{Files: []string{filepath.Join(main, "docker", "compose.yml")}, ProjectDir: "docker"})
	checkSubfolderProject(t, p, err, wt)
}
