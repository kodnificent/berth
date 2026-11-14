package cli

import (
	"fmt"
	"io"
	"os"
	"slices"
	"time"

	"github.com/kodnificent/berth/internal/compose"
	"github.com/kodnificent/berth/internal/docker"
	"github.com/kodnificent/berth/internal/image"
	"github.com/kodnificent/berth/internal/registry"
	"github.com/kodnificent/berth/internal/worktree"
)

// imageCheckDelay makes add wait before comparing build inputs, so tests can stand in
// for a build context that takes that long to compare.
const imageCheckDelay = "BERTH_TEST_IMAGE_CHECK_DELAY"

const wholeRepoNote = "the build context is the whole repository with no .dockerignore, so the berth's own .env and " + OverrideFile + " are build inputs; add a .dockerignore that leaves out what the build doesn't need"

// shareImages gives the berth main's built image, under the name the berth's compose
// uses, for each service whose build inputs match main's, so up in the berth builds only
// when it would in main. It never touches a name the berth already has: that image is
// one the berth built or was given, and replacing it would be build work main wouldn't
// do. Docker being unreachable is not an error; compose then builds on up. What it found
// for each service goes in rec.Builds, for status.
func shareImages(stderr io.Writer, w *worktree.Worktree, plan *composePlan, rec *registry.Record) {
	if plan.main == nil || plan.own == nil {
		return
	}
	if d, err := time.ParseDuration(os.Getenv(imageCheckDelay)); err == nil {
		time.Sleep(d)
	}
	// Loaded again, as compose reads it now: the berth's .env and override are written.
	own, err := compose.Load(w.Path, compose.Options{ComposeFileEnv: true})
	if err != nil || own == nil {
		fmt.Fprintf(stderr, "berth: could not read the berth's compose project to share main's images: %v\n", err)
		return
	}
	mains := map[string]compose.Build{}
	for _, b := range plan.main.Builds(w.Repository) {
		mains[b.Service] = b
	}
	before := map[string]registry.Build{}
	for _, b := range rec.Builds {
		before[b.Service] = b
	}
	var builds []registry.Build
	defer func() { rec.Builds = builds }()
	berthTree, mainTree := image.NewTree(w.Path), image.NewTree(w.Repository)
	for _, b := range own.Builds(w.Path) {
		m, ok := mains[b.Service]
		if !ok || m.Image == b.Image {
			continue
		}
		check := registry.Build{Service: b.Service}
		have, err := docker.ImageExists(b.Image)
		if err != nil {
			fmt.Fprintf(stderr, "berth: could not check images, so up may build %s: %v\n", b.Service, err)
			// What the last add found still stands for status.
			builds = rec.Builds
			return
		}
		switch {
		case have:
			if prev, ok := before[b.Service]; ok {
				check = prev
			} else if slices.Contains(rec.Images, b.Image) {
				check.Shared = true
			} else {
				check.Reason = fmt.Sprintf("the berth has its own image %s", b.Image)
			}
		case !imageExists(m.Image):
			check.Reason = fmt.Sprintf("main has no image %s to share", m.Image)
		default:
			check = compareBuild(stderr, w, b, berthTree, m, mainTree)
			if check.Shared {
				if err := docker.TagImage(m.Image, b.Image); err != nil {
					fmt.Fprintf(stderr, "berth: could not give the berth main's image %s, so up will build %s: %v\n", m.Image, b.Service, err)
					check.Shared, check.Reason = false, fmt.Sprintf("could not tag main's image %s: %v", m.Image, err)
				} else {
					rec.Images = union(rec.Images, []string{b.Image})
				}
			}
		}
		builds = append(builds, check)
	}
}

// compareBuild decides whether the berth's build of a service reads what main's does,
// and says on stderr when it won't share main's image for a reason the user can fix.
func compareBuild(stderr io.Writer, w *worktree.Worktree, b compose.Build, bt *image.Tree, m compose.Build, mt *image.Tree) registry.Build {
	check := registry.Build{Service: b.Service}
	res, err := image.Compare(b, bt, m, mt, image.DefaultLimits)
	check.ReadBytes = res.Read
	if err != nil {
		fmt.Fprintf(stderr, "berth: could not read %s's build inputs, so up will build it: %v\n", b.Service, err)
		check.Reason = fmt.Sprintf("could not read the build inputs: %v", err)
		return check
	}
	whole := image.ReadsWholeRepo(m, w.Repository)
	switch {
	case res.Same:
		check.Shared = true
		return check
	case res.Skipped && whole:
		check.Reason = fmt.Sprintf("not compared with main's, since %s; %s", res.Reason, wholeRepoNote)
	case res.Skipped:
		check.Reason = fmt.Sprintf("not compared with main's, since %s; a .dockerignore that leaves out what the build doesn't need lets berth compare", res.Reason)
	case whole:
		check.Reason = fmt.Sprintf("build inputs differ from main's: %s; %s", res.Reason, wholeRepoNote)
	default:
		check.Reason = fmt.Sprintf("build inputs differ from main's: %s", res.Reason)
		return check
	}
	fmt.Fprintf(stderr, "berth: up will build %s in this berth: %s\n", b.Service, check.Reason)
	return check
}

func imageExists(ref string) bool {
	have, err := docker.ImageExists(ref)
	return err == nil && have
}
