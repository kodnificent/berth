//go:build e2e

package e2e

import (
	"path/filepath"
	"strings"
	"testing"
)

// uncommentMySQL removes one level of comment from the commented [state.mysql] table,
// as someone following its reason would.
func uncommentMySQL(t *testing.T, content string) string {
	t.Helper()
	before, table, ok := strings.Cut(content, "# [state.mysql]\n")
	if !ok {
		t.Fatalf("no commented [state.mysql] table:\n%s", content)
	}
	lines := strings.Split("# [state.mysql]\n"+table, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimPrefix(line, "# ")
	}
	return before + strings.Join(lines, "\n")
}

func TestInitCommentedMySQLTableWithAnUnsetPrefixPassesDoctorOnceUncommented(t *testing.T) {
	env := strings.Replace(laravelEnv, "DB_HOST=127.0.0.1\n", "DB_HOST=db\n", 1)
	env = strings.Replace(env, "DB_DATABASE_PREFIX=app_\n", "", 1)
	repo := presetRepo(t, laravelFiles(), env)

	content := uncommentMySQL(t, initRun(t, repo, repo.Dir).Config)
	if mysql := decodePresets(t, content).State.MySQL; mysql == nil || mysql.Prefix != "" {
		t.Errorf("uncommenting the table turned on a prefix main's .env doesn't set: %+v\n%s", mysql, content)
	}
	writeFile(t, filepath.Join(repo.Dir, "berth.toml"), content)
	expectClean(t, doctor(t, repo, nil, repo.Dir), "with the commented table uncommented")
}

func TestInitConfigPassesAddWhenAPresetKeyHoldsACopiedDatabasesName(t *testing.T) {
	env := strings.Replace(laravelEnv, "REDIS_PREFIX=app_database_\n", "REDIS_PREFIX=app_central\n", 1)
	repo := presetRepo(t, laravelFiles(), env)
	m := startHostMySQL(t, repo, "")
	m.create(t, "app_central")
	pointMainAt(t, repo, m)

	content := initRun(t, repo, repo.Dir).Config
	if _, active := decodePresets(t, content).Env.Set["REDIS_PREFIX"]; active {
		t.Errorf("REDIS_PREFIX is active though main's value names a copied database:\n%s", content)
	}
	repo.Git(t, "add", "berth.toml")
	repo.Git(t, "commit", "-q", "-m", "berth init")
	mustBerth(t, repo, repo.AddWorktree(t, "feat-x"), "add", "--wait")
}
