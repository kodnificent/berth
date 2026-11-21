# 0057. Releases from a tag, and install scripts that download them

Status: accepted

## Context
The only way to install berth was `go install`, which needs a Go toolchain and reports a pseudo-version until a tag exists. The design lists GitHub releases and a `curl | sh` installer among the ways berth is distributed (design, Distribution). Berth runs on macOS, Linux and Windows ([0048](0048-windows.md)), and it has no cgo, so every target cross-compiles from one machine.

## Decision
- **A tag makes a release.** Pushing a tag `v*` runs `.github/workflows/release.yml`, which runs `go vet` and the unit tests, then GoReleaser (`.goreleaser.yaml`). It builds `darwin`, `linux` and `windows` for `amd64` and `arm64` with `CGO_ENABLED=0`, stamps `main.version` with the tag without its `v`, and publishes archives and `checksums.txt` on a GitHub release. A tag with a pre-release suffix, such as `v0.1.0-rc.1`, is published as a pre-release.
- **Archive names carry no version:** `berth_<os>_<arch>.tar.gz`, and `.zip` on Windows. `releases/latest/download/<name>` then always resolves to the newest release, so the install scripts need no GitHub API call, and no token or rate limit.
- **Two install scripts, one per shell,** in `site/public`, so the site serves them at `/install` and `/install.ps1`:
  - `install` is POSIX `sh` for macOS and Linux. It uses `curl` or `wget`, and `sha256sum` or `shasum`, and installs to `$HOME/.local/bin`.
  - `install.ps1` is for Windows PowerShell 5.1 and later. It installs to `%LOCALAPPDATA%\Programs\berth` and adds that folder to the user's `PATH`.
  - Both check the archive against `checksums.txt` and install nothing when it differs or isn't listed. Neither asks for an administrator. `BERTH_VERSION` picks a tag, `BERTH_INSTALL_DIR` the folder, and `BERTH_DOWNLOAD_BASE` another place to download from, for a mirror or a test.
  - Each script's work is in one function called on its last line, so a download cut short runs nothing.
- **The release installs itself.** After publishing, the workflow runs both scripts against the new tag on Linux, macOS and Windows runners and checks `berth --version`.
- **Not signed.** The binaries are not code-signed or notarised. `curl` and PowerShell downloads carry no quarantine mark, so the scripts' installs run; a binary downloaded in a browser meets Gatekeeper or SmartScreen.

## Options considered
- A hand-written build matrix in the workflow: no dependency on GoReleaser, but the archive layout, checksums and release upload would each be code here to keep right.
- Versioned archive names: conventional, but the scripts would have to ask the GitHub API which release is latest, which is rate limited for anonymous callers.
- One shell script for Windows too, run in Git Bash: berth already needs Git for Windows for hooks, but Windows users mostly work in PowerShell.
- Installing to `/usr/local/bin`: on most machines it needs `sudo`, and the script would be asking for a password it doesn't need.
- Homebrew, winget, scoop and an npm wrapper: each needs an account, a repository or a review outside this one. They can be added on top of the same releases.
- Signing and notarising: needs an Apple Developer account and a Windows certificate.

## Evidence
All six targets build with `CGO_ENABLED=0` (`go build` for each, run locally). The shell script was run on macOS with `curl` and `shasum`, and in a `busybox:1.36.1` container on Linux with `wget` and `sha256sum`, against an archive built by hand to the names above: it installs and reports the stamped version, piped into `sh` as well as run from a file; an archive changed after its checksum was written is refused with nothing installed; a missing release exits `1`. `sh -n` and `dash -n` accept it.

The first tag, `v0.1.0`, ran the workflow end to end: GoReleaser published the six archives and `checksums.txt`, and the install job passed on Linux, macOS and Windows, each reporting `berth 0.1.0`.

## Undoing it
Delete `.goreleaser.yaml`, `.github/workflows/release.yml` and the two scripts in `site/public`. Releases already published stay on GitHub, and `go install` is unaffected.
