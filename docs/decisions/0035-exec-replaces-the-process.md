# 0035. exec replaces berth with the command

Status: accepted

## Context
`berth exec [path] -- <cmd>` runs a command with the berth's variables set, for tools that ignore `.env`. The command must run in the worktree, read the stdin `berth exec` was given, and give back its stdout, stderr and exit code unchanged, and SIGINT and SIGTERM sent to `berth exec` must reach it. Decision 0003 kept stdin out of berth entirely, and decision 0002 gave every exit code a berth meaning, so both needed an exception.

## Decision
On Unix, once berth has found the berth and built its variables, it changes to the directory the command runs in, looks the command up from there, so `./bin/tool` is the worktree's, and calls `execve` with `PWD` set to that directory, so the command takes over berth's process: its pid, its stdin, stdout and stderr, its place in the terminal's process group, and its exit status. Berth never reads stdin itself; the command inherits it. Where a process cannot be replaced, berth runs the command as a child with its stdin, stdout and stderr, ignores console interrupts, which reach the child anyway, and exits with the child's code.

The variables are the process environment with every key `berth env` prints removed, then set to the berth's values, so a shadowed key is overridden. The command runs in the current directory when that is inside the worktree, and at the worktree's top otherwise.

Exit codes from `exec` are the command's, and a command may exit `1` just as berth does. Berth's own failures (no berth, a usage error, a command not on `PATH`) happen before the command runs and keep their codes; with `--json` they print the error envelope on stdout, and the command's own failures print nothing extra, so the envelope is what tells the two apart.

This supersedes "berth never reads stdin" in 0003 for `exec`, which hands its stdin to the command and still never reads it, and amends 0002: `exec`'s exit codes are the command's.

## Options considered
- A child process with SIGINT and SIGTERM forwarded: a terminal's Ctrl-C goes to the whole foreground process group, so the child gets it from the terminal and again from berth. A command that treats a second Ctrl-C as "force", such as `docker compose up`, would skip its graceful stop. Telling a terminal's signal from a `kill` needs the sender's pid, which Go's signal package does not give.
- A child in its own process group, with signals forwarded: Ctrl-C arrives once, but the child is no longer in the terminal's foreground group, so an interactive command stops with SIGTTIN when it reads the terminal, unless berth also takes over job control.
- Printing a shell line to eval instead: that is `berth env`, which already exists.

## Evidence
A spike sent SIGINT to the process group, as a terminal's Ctrl-C does, of a command that counts the SIGINTs it handles, ten times each way: run behind a Go parent forwarding SIGINT it handled two every time, run directly it handled one, and run under `berth exec` it handled one. `e2e/exec_test.go` checks the variables, the directory, the exit code, byte-exact stdout, stderr and stdin, SIGINT and SIGTERM sent to the `berth exec` pid alone, shadowed keys, and the `no_berth` cases with and without `--json`. Another test runs `berth exec -- true` with a stdin that never closes.

## Undoing it
`runCommand` in `internal/cli/exec_unix.go` would start a child instead, forward SIGINT and SIGTERM, and return its exit code as `exec_other.go` does, accepting a doubled Ctrl-C from a terminal.
