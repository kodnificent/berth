# 0056. Human output: add's summary, terminal detection and styling

Status: accepted

## Context
`berth add` printed one line and left the reader to run `berth status` for the berth's URL and ports. The design's Human output part now has `add` end with a summary whose rows read as status's own, and has `add` and `status` mark each state with a tick and a colour on a terminal, with no escape codes anywhere else. Status printed its table straight into a `text/tabwriter`, which counts an escape code as text.

## Decision
- **One list of rows:** `status` builds a list of rows (label, value, what the row is, and the state it reports) in `berthView.rows`, from the same values `--json` is made of. `add` builds the same list after its clones are started, or waited for, and prints the rows `summaryRows` keeps, so a summary row can't read differently from status's. Notes, logs and `post-add`'s captured output stay in `status`.
- **Plain rendering:** off a terminal, the rows go through `text/tabwriter` with the settings status always used, so status's bytes are unchanged and the summary lines up the same way.
- **Styled rendering:** on a terminal, berth pads the label column itself, by rune count, and writes each label dimmed (`ESC[2m`). A row that reports a state gets a glyph and a colour on its first word: `✓` green (`32`) for ready or `ok`, `…` yellow (`33`) for cloning, a database still being created, and a pending or running `post-add`, `✗` red (`31`) for anything else, a skipped `post-add` included. A SQLite file main does not have reads `none` and carries no mark. Only the 8 basic colours, which every terminal that takes escape codes maps to its own palette.
- **When to style:** only when standard output is an `*os.File` that is a terminal (`golang.org/x/term`), `NO_COLOR` is not set to a non-empty value (no-color.org) and `TERM` is not `dumb`. On Windows the handle must be a console and take `ENABLE_VIRTUAL_TERMINAL_PROCESSING`, which berth turns on and leaves on; a console that refuses it gets plain rows. A Windows terminal that hands berth a pipe, as Git Bash's does, gets plain rows too.
- **`add --wait` after a failed clone:** the summary prints with the failed row, then the error and exit `4` as before. With `--json` it prints the error envelope alone, as before.

## Options considered
- Styling inside `tabwriter` with its escape marker: escaped text still counts toward the cell's width, so every styled row would sit out of line.
- A styling library (lipgloss, fatih/color): three colours and a dim do not need one, and each brings its own terminal detection and environment variables, which the design does not offer.
- Detecting the terminal with `os.File.Stat` and `ModeCharDevice`: true for `/dev/null` and other character devices that are not terminals.
- A summary of its own format: a second format to keep in step with status, and agents would have to learn both.
- `FORCE_COLOR`, `CLICOLOR` or a `--color` flag: the design keeps colour to what the terminal and `NO_COLOR` say.
- Colouring the whole value: a long error or a path in red reads worse than the one word that names the state.

## Evidence
`e2e/human_output_test.go` covers the summary against `status` and `status --json`, cloning and `--wait`, the second `add`, plain output through a pipe and from the git hook, status's unchanged bytes and unchanged `--json`. `e2e/human_output_tty_test.go` runs `add` and `status` on a pseudo-terminal (`github.com/creack/pty`, tests only, Unix only) for a ready, a cloning and a failed clone, and with `NO_COLOR` and `TERM=dumb`. `internal/cli/human_test.go` covers the host row when there is no URL, the mark each status gets, the glyph and colour on each state's row, and that styling adds only glyphs and escape codes to the plain rows. `e2e/human_output_json_test.go` covers `add --wait --json` after a failed clone. No test drives a real Windows console: `internal/cli/term_windows_test.go` covers a handle that is no console, a console with escape codes on, one that takes them and one that refuses them, through stand-ins for the two console calls, and the Windows runs cover the pipe case.

## Undoing it
`internal/cli/human.go`, `term_other.go` and `term_windows.go`, the `writeRows` calls in `runAdd` and `runStatus`, and the `golang.org/x/term` and `github.com/creack/pty` requirements. Scripts that read `add`'s first line keep working either way; the summary is human output, not a schema.
