// Package privilege lets berth proxy give up root, which it may have only so it can bind
// ports the system keeps for administrators, before it touches anything of the user's.
package privilege

import (
	"errors"
	"fmt"
	"os"
	"strconv"

	"github.com/kodnificent/berth/internal/apperr"
)

// sudoUser is the user who ran sudo, from the variables sudo sets for the command it
// runs. Root itself is no answer: the point is to stop being root.
func sudoUser(getenv func(string) string) (uid, gid int, err error) {
	uid, uerr := strconv.Atoi(getenv("SUDO_UID"))
	gid, gerr := strconv.Atoi(getenv("SUDO_GID"))
	if uerr != nil || gerr != nil || uid <= 0 || gid < 0 {
		return 0, 0, apperr.New(apperr.Generic, "berth proxy is running as root, but SUDO_UID and SUDO_GID name no user other than root, so it can't tell whose berths to serve, and it never serves them as root").
			WithNext("run it with sudo from your own account, as the hint berth prints when a port needs it")
	}
	return uid, gid, nil
}

// rootByItself refuses an effective root over another real uid, a setuid-root berth: the
// sudo variables are the caller's to set, so only a process that is root through and
// through may believe them.
func rootByItself(realUID int) error {
	if realUID != 0 {
		return apperr.New(apperr.Generic, "berth proxy is running with root's privileges for user %d, as a setuid program, and berth never runs that way", realUID).
			WithNext("remove the setuid bit from berth, and run berth proxy with sudo when a port needs it")
	}
	return nil
}

var errRegained = errors.New("the process could become root again after giving it up")

func dropFailed(err error) error {
	return apperr.New(apperr.Generic, "berth proxy could not give up root, so it serves nothing: %v", err)
}

// stateOwner refuses a state directory that exists and belongs to anyone but uid: the
// proxy would read another user's berths, or write where that user can't replace it.
func stateOwner(dir string, uid int, owner func(os.FileInfo) (int, bool)) error {
	info, err := os.Stat(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if got, ok := owner(info); ok && got != uid {
		return apperr.New(apperr.Generic, "berth's state directory %s belongs to user %d, not to %d, who ran sudo, so berth proxy serves nothing from it", dir, got, uid).
			WithNext(fmt.Sprintf("run it with sudo from the account that owns %s, keeping the HOME and variables berth's hint prints", dir))
	}
	return nil
}
