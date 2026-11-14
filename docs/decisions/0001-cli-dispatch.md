# 0001. CLI dispatch with the standard library

Status: accepted

## Context
Berth needs a `berth` command with subcommands, `--version`, and `--json` on every command, with full control over where output goes and which exit code each failure produces (design, Commands > Conventions). A CLI framework decides some of this for you.

## Decision
`internal/cli` holds a small dispatcher on the standard library: global flags (`--version`, `-h`/`--help`, `--json`) come before the command name, commands register in a list with a name, summary and run function, and each command parses its own arguments. `--json` is accepted anywhere before a `--` separator, so an agent can append it to any command line.

Help is dispatch's job too. Each command carries a usage text. When `-h` or `--help` appears anywhere among a visible command's arguments before a `--`, dispatch prints that command's usage, its flags and the global ones, and exits `0` without running the command. With `--json` it prints `{"command", "summary", "usage"}`. Hidden commands, which berth runs itself, get no help.

## Options considered
- cobra: mature, but prints its own usage and errors to stderr and returns untyped errors, so the JSON error envelope and exit codes would fight its defaults; it also pulls in several dependencies for a few hundred lines of dispatch.
- urfave/cli: same trade-off as cobra.
- stdlib `flag.FlagSet` per command plus a hand-written dispatcher: chosen. Commands can still use `flag.FlagSet` for their own flags.
- Teaching each command's flag parsing about `-h`: every command would repeat it, and a new one could forget.
- A flag library with generated help: berth's commands parse a handful of flags by hand, and a library would change how every unknown flag is reported.

## Evidence
The whole dispatcher, with help, is `internal/cli/cli.go`, about 230 lines. `cmd/berth/main_test.go` exercises it through the built binary, and asks for help on every command `berth --help --json` lists.

## Undoing it
Swap `internal/cli.Run` for a cobra root command. Each command's run function keeps its signature; the work is wiring `--json` and routing errors through `report` so codes and exit codes stay as recorded in 0002. Help is `wantsHelp` and `printCommandHelp` in `internal/cli/cli.go`, and the `Usage` field of each command.
