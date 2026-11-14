package dockerfile

import (
	"reflect"
	"testing"
)

func TestAnalyze(t *testing.T) {
	for _, tc := range []struct {
		name, dockerfile, target string
		want                     Reads
	}{
		{
			name:       "single stage",
			dockerfile: "FROM busybox:1.36.1\nCOPY index.html /www/\nCOPY [\"conf/a b\", \"/etc/\"]\n",
			want:       Reads{Paths: []string{"index.html", "conf/a b"}, Contexts: []string{"busybox:1.36.1"}},
		},
		{
			name: "target skips stages it does not reach",
			dockerfile: `FROM php:8.3 AS base
COPY --from=composer:2 /usr/bin/composer /usr/bin/composer
FROM base AS conf
COPY .docker/php/php.ini $PHP_INI_DIR/php.ini
FROM conf AS build
COPY . /var/www
FROM base AS dev
COPY .docker/bin/ /usr/local/bin/
FROM conf AS prod
COPY --from=build /var/www /var/www
`,
			target: "DEV",
			want:   Reads{Paths: []string{".docker/bin/"}, Contexts: []string{"php:8.3", "composer:2"}},
		},
		{
			name:       "last stage by default, through --from",
			dockerfile: "FROM alpine AS a\nCOPY a.txt /\nFROM alpine AS b\nCOPY b.txt /\nFROM scratch\nCOPY --from=0 /a.txt /\n",
			want:       Reads{Paths: []string{"a.txt"}, Contexts: []string{"scratch", "alpine"}},
		},
		{
			name:       "bind mount from the context",
			dockerfile: "FROM alpine\nRUN --mount=type=cache,target=/c --mount=type=bind,source=go.sum,target=/s true\nRUN --mount=type=secret,id=x true\n",
			want:       Reads{Paths: []string{"go.sum"}, Contexts: []string{"alpine"}},
		},
		{
			name:       "bind mount without a source reads everything",
			dockerfile: "FROM alpine\nRUN --mount=target=/src make\n",
			want:       Reads{Whole: true},
		},
		{
			name:       "heredocs and remote sources are not the context",
			dockerfile: "FROM alpine\nCOPY <<EOF /x\nhi\nEOF\nADD https://example.com/a.tgz /\n",
			want:       Reads{Contexts: []string{"alpine"}},
		},
		{
			name:       "a build arg in a source",
			dockerfile: "FROM alpine\nARG DIR\nCOPY ${DIR} /\n",
			want:       Reads{Whole: true},
		},
		{
			name:       "a build arg in FROM",
			dockerfile: "ARG BASE=alpine\nFROM $BASE\nCOPY a /\n",
			want:       Reads{Whole: true},
		},
		{
			name:       "an unknown target",
			dockerfile: "FROM alpine AS a\nCOPY a /\n",
			target:     "b",
			want:       Reads{Whole: true},
		},
		{
			name:       "another frontend",
			dockerfile: "# syntax=example.com/frontend:1\nFROM alpine\nCOPY a /\n",
			want:       Reads{Whole: true},
		},
		{
			name:       "the stock frontend",
			dockerfile: "# syntax=docker/dockerfile:1\nFROM alpine\nCOPY a /\n",
			want:       Reads{Paths: []string{"a"}, Contexts: []string{"alpine"}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := Analyze([]byte(tc.dockerfile), tc.target); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Analyze = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestClean(t *testing.T) {
	for in, want := range map[string]string{".": ".", "/": ".", "./a/../b/": "b", "/etc/x": "etc/x", "a/*.txt": "a/*.txt"} {
		if got := Clean(in); got != want {
			t.Errorf("Clean(%q) = %q, want %q", in, got, want)
		}
	}
}
