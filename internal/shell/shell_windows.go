package shell

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

// Path is the sh scripts run with: the one Git for Windows installs, found through the
// git on PATH, so a script written for macOS and Linux runs unchanged.
var Path = sync.OnceValues(func() (string, error) {
	git, err := exec.LookPath("git")
	if err != nil {
		return "", fmt.Errorf("berth runs scripts with the sh that Git for Windows installs, and finds it through git, which is not on PATH; install Git for Windows")
	}
	if out, err := exec.Command(git, "--exec-path").Output(); err == nil {
		// git-core sits three levels under the install, in mingw64\libexec.
		if sh, ok := gitSh(filepath.FromSlash(strings.TrimSpace(string(out))), 4); ok {
			return sh, nil
		}
	}
	// git.exe sits in cmd or bin, one level under the install.
	if sh, ok := gitSh(filepath.Dir(git), 2); ok {
		return sh, nil
	}
	return "", fmt.Errorf("berth runs scripts with the sh that Git for Windows installs, and the git at %s has no sh.exe; install Git for Windows, which comes with it", git)
})

// scriptVar and argVar carry the script and its $1 and on into sh: Git for Windows' sh
// rewrites the command line it is started with, dropping quotes, expanding braces and
// wildcards and collapsing doubled backslashes, so neither would arrive as given, and the
// environment is left alone. They are taken out again before the script runs, so nothing
// it starts inherits them. $0 stays on the command line, since only sh -c can set it, and
// a command script's mark there is how recovery recognises the run.
const (
	scriptVar = "BERTH_SH_SCRIPT"
	argcVar   = "BERTH_SH_ARGC"
	argVar    = "BERTH_SH_ARG"
)

const runScript = `__berth_script=$` + scriptVar + `; unset ` + scriptVar + `; set --; __berth_i=0; ` +
	`while [ "$__berth_i" -lt "$` + argcVar + `" ]; do __berth_i=$((__berth_i + 1)); ` +
	`eval "set -- \"\$@\" \"\$` + argVar + `$__berth_i\"; unset ` + argVar + `$__berth_i"; done; ` +
	`unset ` + argcVar + ` __berth_i; eval "$__berth_script"`

func command(sh string, env []string, script string, args []string) *exec.Cmd {
	if env == nil {
		env = os.Environ()
	}
	argv := []string{"-c", runScript}
	var rest []string
	if len(args) > 0 {
		argv, rest = append(argv, args[0]), args[1:]
	}
	env = append(append([]string{}, env...), scriptVar+"="+script, argcVar+"="+strconv.Itoa(len(rest)))
	for i, a := range rest {
		env = append(env, argVar+strconv.Itoa(i+1)+"="+a)
	}
	cmd := exec.Command(sh, argv...)
	cmd.Env = env
	return cmd
}
