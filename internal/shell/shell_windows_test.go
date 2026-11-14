package shell

import "testing"

func TestAScriptReachesShExactlyAsWritten(t *testing.T) {
	cmd, err := Command(nil, `printf '%s' 'a\\b [*]'`)
	if err != nil {
		t.Fatal(err)
	}
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(out), `a\\b [*]`; got != want {
		t.Errorf("the script printed %q, want %q", got, want)
	}
}

func TestAScriptDoesNotLeaveItselfOrItsArgumentsInItsEnvironment(t *testing.T) {
	cmd, err := Command(nil, `env | grep '^BERTH_SH_' || true`, "name", "an argument")
	if err != nil {
		t.Fatal(err)
	}
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	if got := string(out); got != "" {
		t.Errorf("the script found %q in its environment, want none of it", got)
	}
}
