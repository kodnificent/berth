// Package cookie judges the cookies an app sets against the berth's host, whoever saw
// them: doctor's probe, or a proxy in front of the app.
package cookie

import (
	"net/http"
	"strings"

	"github.com/kodnificent/berth/internal/names"
)

// Domain is c's Domain= attribute as browsers read it, without a leading dot and in
// lowercase, empty for a host-only cookie.
func Domain(c *http.Cookie) string {
	return strings.ToLower(strings.TrimPrefix(c.Domain, "."))
}

// Wider reports whether c reaches more than host: its domain is a name host is under,
// such as myapp.localhost or localhost for feat-x.myapp.localhost, so a browser sends it
// to every berth under that name too. Host itself or a name under it stays the berth's.
func Wider(host string, c *http.Cookie) bool {
	d := Domain(c)
	return d != "" && names.Within(d, host) && !names.Within(host, d)
}
