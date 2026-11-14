# 0013. The managed .env block

Status: accepted

## Context
Berth writes the berth's values into `.env` between markers and never touches anything outside them, and writes a key only when it changed the value that key depends on (design, Configuration > The managed `.env` block). Each key set in the block must win when Docker Compose or a dotenv parser reads the file, a repeated `add` must change no file, `berth env` must print the same keys, and removal must later restore `.env` outside the block byte for byte. The design leaves open where the block goes, how values are quoted, what happens when there is no key to write, and what happens to a `.env` git tracks.

## Decision
- **Where:** the block is always the last lines of the worktree's root `.env`. On every `add` any existing block is taken out, wherever it is, and the new one appended, so a line a user adds after it doesn't push the block up. When the content before the block has no final newline, berth adds one and records that in the berth's registry record (`env_separator`), so taking the block out restores the file exactly.
- **When:** `add` writes `.env` only when its bytes would change, to a temporary file renamed into place with the old file's mode. A worktree with no `.env`, even after the source's has been cloned, gets one holding just the block, recorded as created by berth.
- **Nothing to set:** when the berth has no key to write, `add` writes no block. A worktree with no `.env` gets none, a `.env` it has is left as it is, and a block left by an earlier `add` is taken out, with its separator newline, when the config stops setting its last key. `status` lists `.env` under `files` only when the block holds a key or berth created the file.
- **Keys:** every key under `[env] url`, `[env] host` and `[env.set]`, expanded as in 0009, plus the `env` of each `[ports]` entry holding its assigned port. `[env.set]` wins over the others. Keys are sorted. `status --json` reports the same map as `env`, and `berth env` prints it. Later records add keys of their own, such as the compose keys of 0017.
- **Quoting:** a value of letters, digits and `_./:,@%+=-` is written bare; otherwise in single quotes; a value holding a single quote is written in double quotes only if it holds none of `" $ \` `` ` `` or a newline; anything else is refused with `config_invalid`, naming the key.
- **Tracked `.env`:** `add` refuses before provisioning anything, exit 1, and says how to untrack it. Writing the block would change a committed file.
- **`berth env`:** prints `export KEY='value'` lines, single quotes escaped as `'\''`, for `eval "$(berth env)"` in a POSIX shell; with `--json`, `{"env": {...}}`. Values come from config and the berth's record, the same source `add` wrote from.

## Options considered
- Block at the top of `.env`: Compose, phpdotenv, Node dotenv and godotenv all let the last duplicate win, so a key the user also sets lower down would beat berth's. A project whose own scripts read `.env` by hand tends to take the last line too.
- Leave the block where it is once written: a line appended after it by a tool would break both of the above.
- Always double-quote with escapes: Compose and phpdotenv interpolate `$` inside double quotes and disagree on escapes, so some values would be read differently by different tools.
- Mark `.env` skip-worktree when it is tracked: hides a real change from git status and surprises anyone who later edits the file.
- Read `berth env` back from the block: would print a stale value after a config change without `add`, while `status` printed the new one; one source for both keeps them agreeing.
- Keep an empty block as a sign that the worktree has a berth: the registry and the worktree's marker already say so (0004), and no command reads the block for that.
- Write the empty block only into a `.env` the worktree already has: still a change to a file with nothing in it for any tool to read.
- Delete a `.env` berth created once its block is empty: the user may have added lines of their own since, and `remove` already deletes a file berth recorded as created.

## Evidence
`e2e/files_test.go`: with the source `.env` setting the same keys first, `docker compose config --environment` and godotenv both read the block's values, including one with spaces, `#`, `$` and double quotes; a second `add` leaves the size, mode, modification time and hash of every file in the worktree, main, the state directory and `.git/info` unchanged. `e2e/empty_block_test.go` covers a berth with nothing to set: no `.env` written, a cloned `.env` equal to main's byte for byte, the block going with its last key and coming back with it, and a `.env` berth created keeping a line added since. `internal/envfile` tests cover the separator newline, moving the block last, markers inside a value, and quoting. A block with a bare URL, an empty value, a single-quoted value with `#`, `$` and `"`, and a double-quoted value with `'`, appended after the same keys, was read identically by phpdotenv 5.6.2 (immutable repository, as Laravel uses it) and Node dotenv 16.6.1.

## Undoing it
The block format and quoting live in `internal/envfile`, the keys in `managedEnv` in `internal/cli/files.go`. Moving the block or changing its markers needs `add` to recognise the old markers to take the old block out. The no-block case is the `switch` on the managed keys in `materialize` in `internal/cli/files.go`, and `viewBerth` in `internal/cli/berths.go` for `status`; going back writes the empty block again on the next `add`.
