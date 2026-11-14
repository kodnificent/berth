package cli

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/kodnificent/berth/internal/cookie"
	"github.com/kodnificent/berth/internal/proxy"
	"github.com/kodnificent/berth/internal/registry"
)

// cookieWatch says on log each cookie a berth's app sets wider than the berth's host, and
// records it for berth doctor. Recording happens on a goroutine of its own, so a response
// never waits on the registry lock or the disk.
type cookieWatch struct {
	log  io.Writer
	jobs chan cookieJob

	mu   sync.Mutex
	said map[string]bool

	// lists is what is recorded for each berth, as last written; only record touches it.
	lists map[string][]cookie.Seen
	// failed is the last error said for each berth, so one that persists is said once.
	failed map[string]string
}

type cookieJob struct {
	id   string
	seen proxy.SetCookie
}

func newCookieWatch(log io.Writer) *cookieWatch {
	return &cookieWatch{log: log, jobs: make(chan cookieJob, 1024), said: map[string]bool{}, lists: map[string][]cookie.Seen{}, failed: map[string]string{}}
}

// hear is the proxy's Cookies hook. A cookie lost because the queue is full is heard
// again the next time the app sets it.
func (w *cookieWatch) hear(b proxy.Berth, c proxy.SetCookie) {
	if c.Wide {
		w.say(b, c)
	}
	if b.ID == "" {
		return
	}
	select {
	case w.jobs <- cookieJob{b.ID, c}:
	default:
	}
}

func (w *cookieWatch) say(b proxy.Berth, c proxy.SetCookie) {
	key := strings.Join([]string{b.ID, b.Host, c.Name, c.Domain, fmt.Sprint(c.Rewritten)}, "\x00")
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.said[key] {
		return
	}
	w.said[key] = true
	what := fmt.Sprintf("scope it to %s or leave Domain out, or set [proxy] cookies = \"rewrite\"", b.Host)
	if c.Rewritten {
		what = fmt.Sprintf("the proxy rewrote it to Domain=%s", b.Host)
	}
	fmt.Fprintf(w.log, "berth proxy: %s: cookie %s, set by %s, has Domain=%s, so a browser sends it to every berth under %s; %s\n",
		b.Name, c.Name, c.URL, c.Domain, cookie.Domain(&http.Cookie{Domain: c.Domain}), what)
}

func (w *cookieWatch) record() {
	for j := range w.jobs {
		w.apply(j)
	}
}

// apply records a wide cookie, or forgets one the app now sets within the berth's host,
// writing only when that changes what is recorded.
func (w *cookieWatch) apply(j cookieJob) {
	list, ok := w.lists[j.id]
	if !ok {
		list, _ = cookie.LoadSeen(j.id)
		w.lists[j.id] = list
	}
	var next []cookie.Seen
	var changed bool
	if j.seen.Wide {
		next, changed = cookie.Note(list, cookie.Seen{Name: j.seen.Name, Domain: j.seen.Domain, URL: j.seen.URL, Rewritten: j.seen.Rewritten, At: time.Now().UTC()})
	} else {
		next, changed = cookie.Clear(list, j.seen.Name)
	}
	if !changed {
		return
	}
	recorded, err := registry.IfRecorded(j.id, func() error { return cookie.SaveSeen(j.id, next) })
	msg := ""
	if err != nil {
		msg = err.Error()
		if w.failed[j.id] != msg {
			fmt.Fprintf(w.log, "berth proxy: could not record a cookie of berth %s for berth doctor: %v\n", j.id, err)
		}
	}
	w.failed[j.id] = msg
	if recorded && err == nil {
		w.lists[j.id] = next
	}
}

// proxyCookieFindings are the cookies berth proxy saw the berth's app set wider than its
// host, reported without any request to the app.
func proxyCookieFindings(rec registry.Record, env map[string]string) []finding {
	list, err := cookie.LoadSeen(rec.ID)
	if err != nil {
		return []finding{{"cookie_domain", notVerified, rec.Worktree, "the cookies berth proxy saw can't be read: " + err.Error(), nil}}
	}
	var out []finding
	for _, s := range list {
		f := cookieDomainFinding(s.URL, rec.Host, &http.Cookie{Name: s.Name, Domain: s.Domain}, env)
		if f == nil {
			continue
		}
		f.Message += fmt.Sprintf("; berth proxy saw the app set it on %s", s.At.Local().Format("2006-01-02 15:04"))
		if s.Rewritten {
			f.Message += fmt.Sprintf(" and rewrote it to Domain=%s", rec.Host)
		}
		out = append(out, *f)
	}
	return out
}
