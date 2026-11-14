package volume

import (
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/kodnificent/berth/internal/docker"
)

// Staging is where a copy lands inside the berth's volume until it is complete, so a
// service started early finds a directory that isn't its data rather than half of it.
const staging = ".berth-partial"

const emptyScript = `set -e
find /to -mindepth 1 -maxdepth 1 -exec rm -rf {} +`

const wipeScript = emptyScript + `
mkdir /to/` + staging

// copyScript reads the source; it is the only step that runs while the source's
// containers are paused. A test delay comes after the copy, so the copy is seen in
// progress with its data already in place.
const copyScript = `set -e
(cd /from && tar -cf - .) | (cd /to/` + staging + ` && tar -xpf - --numeric-owner)
if [ -n "$BERTH_DELAY" ]; then sleep "$BERTH_DELAY"; fi`

// progressScript measures what has reached the berth's volume so far.
const progressScript = `du -sk /to/` + staging + ` 2>/dev/null | cut -f1`

// progressEvery is how often the clone process measures a copy in progress for status.
const progressEvery = 2 * time.Second

const finishScript = `set -e
cd /to/` + staging + `
for f in * .[!.]* ..?*; do
  if [ -e "$f" ] || [ -L "$f" ]; then mv "$f" /to/; fi
done
cd /to && rmdir ` + staging + `
chown "$(stat -c %u:%g /from)" /to
chmod "$(stat -c %a /from)" /to
du -sk /to | cut -f1`

// Run clones every entry of berth id that is still cloning. The caller holds the
// berth's clone lock for as long as Run runs. delay is a pause at the end of each copy
// that tests use to stand in for a large volume.
func Run(id string, delay time.Duration, log io.Writer) error {
	s, err := Load(id)
	if err != nil {
		return err
	}
	for i := range s.Volumes {
		e := &s.Volumes[i]
		if e.Status != Cloning {
			continue
		}
		save := func() {
			if err := Save(id, s); err != nil {
				fmt.Fprintf(log, "saving clone state: %v\n", err)
			}
		}
		start := time.Now()
		fmt.Fprintf(log, "%s cloning %s into %s\n", start.Format(time.RFC3339), e.Source, e.Target)
		err := copyVolume(id, e, delay, save)
		end := time.Now().UTC().Truncate(time.Millisecond)
		e.FinishedAt = &end
		if err != nil {
			e.Status, e.Error = Failed, err.Error()
			fmt.Fprintf(log, "%s failed: %v\n", e.Key, err)
		} else {
			e.Status = Ready
			fmt.Fprintf(log, "%s ready in %s\n", e.Key, time.Since(start).Round(time.Millisecond))
		}
		save()
	}
	return nil
}

func copyVolume(id string, e *Entry, delay time.Duration, save func()) (err error) {
	if e.Source == e.Target {
		return fmt.Errorf("source and target are the same volume %s", e.Source)
	}
	if e.SourceMissing {
		return empty(id, e)
	}
	if err := docker.StartHelper(e.Helper, id, e.Source+":/from:ro", e.Target+":/to"); err != nil {
		return err
	}
	defer func() {
		if rmErr := docker.RemoveHelpers(id); err == nil && rmErr != nil {
			err = rmErr
		}
	}()
	if _, err := docker.Exec(e.Helper, nil, wipeScript); err != nil {
		return err
	}

	running, err := docker.RunningWithVolume(e.Source)
	if err != nil {
		return err
	}
	e.Paused, e.Pausing = nil, nil
	for _, c := range running {
		e.Paused = append(e.Paused, c.Label())
		e.Pausing = append(e.Pausing, c.ID)
	}
	save()

	var env []string
	if delay > 0 {
		env = append(env, "BERTH_DELAY="+strconv.FormatFloat(delay.Seconds(), 'f', 3, 64))
	}
	paused := time.Now()
	err = docker.Pause(e.Pausing...)
	if err == nil {
		stop := measure(e, save)
		_, err = docker.Exec(e.Helper, env, copyScript)
		stop()
	}
	if unErr := docker.Unpause(e.Pausing...); unErr != nil {
		return fmt.Errorf("resuming %s after the copy: %w", strings.Join(e.Paused, ", "), unErr)
	}
	if len(e.Pausing) > 0 {
		ms := time.Since(paused).Milliseconds()
		e.PauseMS = &ms
	}
	e.Pausing = nil
	save()
	if err != nil {
		return err
	}

	out, err := docker.Exec(e.Helper, nil, finishScript)
	if err != nil {
		return err
	}
	kb, _ := strconv.ParseInt(strings.TrimSpace(out), 10, 64)
	e.Bytes, e.Copied = kb*1024, 0
	return nil
}

// empty leaves the berth's volume empty, as main's would be if it existed. The source
// is never mounted: docker run would create it.
func empty(id string, e *Entry) (err error) {
	if err := docker.StartHelper(e.Helper, id, e.Target+":/to"); err != nil {
		return err
	}
	defer func() {
		if rmErr := docker.RemoveHelpers(id); err == nil && rmErr != nil {
			err = rmErr
		}
	}()
	_, err = docker.Exec(e.Helper, nil, emptyScript)
	e.Bytes, e.Copied = 0, 0
	return err
}

// measure records how much of e has been copied every progressEvery until stop is
// called, and returns once it has stopped, so e is the caller's alone again.
func measure(e *Entry, save func()) (stop func()) {
	done := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		tick := time.NewTicker(progressEvery)
		defer tick.Stop()
		for {
			select {
			case <-done:
				return
			case <-tick.C:
				out, err := docker.Exec(e.Helper, nil, progressScript)
				if kb, perr := strconv.ParseInt(strings.TrimSpace(out), 10, 64); err == nil && perr == nil {
					e.Copied = kb * 1024
					save()
				}
			}
		}
	}()
	return func() {
		close(done)
		<-finished
	}
}

// Recover finishes off a clone whose process died: it resumes any source container the
// process left paused, removes its helpers and marks what was cloning as failed. The
// caller holds the berth's clone lock, so no clone process is running. Docker is asked
// only when a volume clone was left unfinished, so a berth that copies only databases
// recovers without it; a server ends a dead process's sessions, and the table locks with
// them, by itself. It reports whether anything changed.
func Recover(id string, s *State) (bool, error) {
	var volumes, databases bool
	for _, e := range s.Volumes {
		volumes = volumes || e.Status == Cloning || len(e.Pausing) > 0
	}
	for _, e := range s.Databases {
		databases = databases || e.Status == Cloning
	}
	if !volumes && !databases {
		return false, nil
	}
	var errs []string
	for i := range s.Volumes {
		e := &s.Volumes[i]
		if err := docker.Unpause(e.Pausing...); err != nil {
			errs = append(errs, err.Error())
		} else {
			e.Pausing = nil
		}
		stopped(e)
	}
	for i := range s.Databases {
		e := &s.Databases[i]
		if e.Status == Cloning && e.ScriptPID > 0 {
			if err := stopScript(e.ScriptPID, e.ScriptMark); err != nil {
				errs = append(errs, err.Error())
			}
			e.ScriptPID, e.ScriptMark = 0, ""
		}
		stopped(e)
	}
	if volumes {
		if err := docker.RemoveHelpers(id); err != nil {
			errs = append(errs, err.Error())
		}
	}
	if err := Save(id, *s); err != nil {
		return true, err
	}
	if len(errs) > 0 {
		return true, fmt.Errorf("recovering a stopped clone: %s", strings.Join(errs, "; "))
	}
	return true, nil
}

// stopped marks e failed if it was still cloning when its process died.
func stopped(e *Entry) {
	if e.Status != Cloning {
		return
	}
	again := "berth add"
	if e.Reset {
		again = "berth reset"
	}
	e.Status = Failed
	e.Error = "the clone process stopped before it finished; run " + again + " to clone again"
}
