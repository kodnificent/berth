# Fixture project

A small compose project the e2e harness copies into a fresh git repository for each test.
It stands in for a typical Laravel compose app: a built `web` service, MySQL with a named data volume, a
`cache` volume, a `search` service that `web` depends on, and a published port that the
`search` healthcheck reads through `SEARCH_PORT` inside the container.

The harness rewrites the pinned `name:` to a unique project per test and writes a
gitignored `.env` with free host ports, so tests can run in parallel.
