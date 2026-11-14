# 0003. Berth never reads stdin

Status: accepted; for `exec`, which hands its stdin to the command and still never reads it, superseded by [0035](0035-exec-replaces-the-process.md)

## Context
The design says berth is non-interactive by default and never prompts from a hook or without a TTY. A git hook or agent harness that runs berth with an open stdin would hang forever if berth waited for an answer.

## Decision
Berth never reads stdin, with or without a TTY. Anything that would need confirmation takes a flag instead. The only commands that touch stdin hand it on unread: `exec` to the command it runs (0035), and `config edit` to the editor it starts (0031).

## Options considered
- Prompt only when stdin is a TTY: still hangs when a harness allocates a pseudo-TTY, and makes behaviour differ between a terminal and a hook, which makes bugs hard to reproduce.
- Read with a timeout: slows every unattended run and still acts on stray input.

## Evidence
`cmd/berth/main_test.go` runs berth's commands, with and without `--json`, with stdin an open pipe that never delivers data or EOF, and fails if any run is alive after five seconds. A new command has to be added to that test's list by hand.

## Undoing it
Read `Context.Stdin` in the command that needs it and gate any prompt on a TTY check plus an explicit flag. The no-TTY test must keep passing.
