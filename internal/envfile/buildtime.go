package envfile

import "strings"

// buildTimePrefixes are the env key prefixes that bundlers read at build time and inline
// into the files they write: Vite and Astro, Next.js, Create React App, SvelteKit and
// Nuxt. A value under one of them is fixed in the build output from then on.
var buildTimePrefixes = []string{"VITE_", "NEXT_PUBLIC_", "REACT_APP_", "PUBLIC_", "NUXT_PUBLIC_"}

// BuildTime reports whether a bundler inlines key into build output.
func BuildTime(key string) bool {
	for _, p := range buildTimePrefixes {
		if strings.HasPrefix(key, p) {
			return true
		}
	}
	return false
}
