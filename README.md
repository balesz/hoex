# hoex

hoex lets an AI agent running in a Docker sandbox
run a small, allow-listed set of commands **on the host**, and get their output back
as if they ran locally.

Some tools only exist or only work properly on the host: the Flutter and Dart
toolchain, platform-specific builds, simulators. Instead of giving the agent full
access to the host, or running those commands by hand, you allow exactly the
commands you want in a config file. Every run is shown live on the host.

```bash
# in the sandbox, run by the agent
hoex --mode sandbox -- flutter test | grep FAIL
```

- **Allow-listed.** Only commands matching your rules run on the host. Anything
  else is rejected before it starts.
- **Transparent.** stdout and stderr are streamed separately, and hoex exits with
  the command's own exit code, so pipes and conditions in the sandbox work as usual.
  Interrupting hoex stops the command on the host.
- **Visible.** The host shows every run and its live output in a terminal UI, and
  keeps an audit log.
- **No shell on the host.** Commands run directly, without a shell: no variable
  expansion, globbing, redirection or pipes on the host.
- **Self-documenting.** `hoex --skill claude` generates a Claude Code skill from
  your rules, so the agent knows which commands to run through hoex.

## How it works

One binary runs in two modes:

| Command | Where | What it does |
| --- | --- | --- |
| `hoex --mode host` | Host, in the project root | Daemon: checks requests against the rules, runs allowed commands in the project, shows runs and output. |
| `hoex --mode sandbox -- <command>` | Sandbox | Sends the command to the host, writes its stdout and stderr, exits with its exit code. |
| `hoex --skill claude` | Host, in the project root | Installs the Claude Code skill and settings into the project. |

The two sides talk gRPC over TCP. The host listens on `127.0.0.1` only (port
`47800` by default), and the sandbox reaches it as `host.docker.internal`. At
startup the host writes the port and a random token to `.hoex/connection` in the
project. The sandbox sees the same project directory at the same path, reads the
file from there, and sends the token with every request.

## Install

Binaries for macOS and Linux (arm64 and amd64) are on the
[releases page](https://github.com/balesz/hoex/releases). You need hoex on the
host **and** in the sandbox (the Linux build).

**Host (macOS or Linux):**

```bash
os=$(uname -s | tr '[:upper:]' '[:lower:]')
arch=$(uname -m); case $arch in x86_64) arch=amd64;; aarch64) arch=arm64;; esac
curl -fsSL "https://github.com/balesz/hoex/releases/latest/download/hoex_${os}_${arch}.tar.gz" \
  | tar -xz -C /usr/local/bin hoex
hoex --version
```

If you download the archive with a browser on macOS, remove the quarantine flag
before the first run: `xattr -d com.apple.quarantine /usr/local/bin/hoex`.

**Sandbox:** run the same commands inside the sandbox (use `sudo tar …` if
`/usr/local/bin` is not writable there, or extract into a directory on your
`PATH`). The sandbox needs network access to `github.com` for the download.

Each release has a `checksums.txt` with the SHA-256 of every archive.

## Quick start

A Flutter project, with `flutter` and `dart` run on the host.

1. **Allow the commands.** Create `.hoex.yaml` in the project root:

   ```yaml
   allow:
     - prefix: flutter
       description: Flutter build, test and run.
       example: flutter test
     - prefix: dart
       description: Dart tools.
       example: dart format .
   ```

2. **Start the host** in the project root, in its own terminal:

   ```bash
   hoex --mode host
   ```

3. **Allow the port in the sandbox network policy** (once per port, on the host):

   ```bash
   sbx policy allow network localhost:47800
   ```

4. **Install the skill** (once per project, on the host):

   ```bash
   hoex --skill claude
   ```

5. **Use it.** Claude Code in the sandbox now runs `flutter` and `dart` commands
   through hoex. You can try it yourself in the sandbox:

   ```bash
   hoex --mode sandbox -- flutter --version
   ```

## Configuration

Rules come from two levels, and they add up:

| Level | File | Applies to |
| --- | --- | --- |
| User | `~/.config/hoex/config.yaml` (or `$XDG_CONFIG_HOME/hoex/config.yaml`) | The projects listed in it by absolute path |
| Project | `.hoex.yaml` in the project root | That project |

**User level.** Use it for rules you want in a project without putting them into
the project, or for projects you do not want to add a file to:

```yaml
projects:
  - path: /Users/you/dev/my_flutter_app
    deny_chaining: true
    allow:
      - prefix: flutter
        description: Flutter build, test and run.
        example: flutter test
```

**Project level:**

```yaml
deny_chaining: true
allow:
  - pattern: 'npm (test|run build)'
    description: Run the tests and the build.
    example: npm test
  - pattern: '\./scripts/release\.sh'
    description: Release script.
    example: ./scripts/release.sh
```

**Rules**

| Key | Meaning |
| --- | --- |
| `prefix` | Allows a command that starts with these words. Whole words only: `flutter` allows `flutter test` but not `flutterx`; `fvm flutter` allows `fvm flutter test` but not `fvm install`. |
| `pattern` | A regular expression ([Go syntax](https://pkg.go.dev/regexp/syntax)) that must match the **whole** command, the arguments joined by single spaces. `^` and `$` are implied. |
| `description` | Required. What the command is for; it goes into the agent skill. |
| `example` | Required. An example command; it goes into the skill. hoex warns if the rule does not allow its own example. |

Each rule has exactly one of `prefix` and `pattern`.

**Other keys** (project level, or per entry under `projects` at user level):

| Key | Default | Meaning |
| --- | --- | --- |
| `deny_chaining` | `true` | Rejects commands whose arguments contain `;`, `&&`, `||`, `|`, a line break, `$(` or a backtick. They would not run anyway without a shell, but this way the agent gets a clear error. It applies to the rules of its own level; if rules of both levels match a command, the stricter setting wins. |
| `port` | `47800` | The port the host listens on. The project level wins over the user level, and `--port` wins over both. Give each project its own port if you run several hosts at once. |

If a config file is invalid (YAML, regular expression, missing or unknown key), the
host does not start and names the file and line.

**Do not allow a shell.** A rule such as `prefix: bash` allows `bash -c '…'`, which
runs anything. hoex warns at startup about rules that allow `bash`, `sh`, `zsh`,
`dash`, `ksh`, `fish`, `env` or `xargs`. Scripts with a shebang line and the
executable bit run without a shell, so allow `./scripts/build.sh` itself instead.

## Config changes need your approval

The agent can write to the project, so the host does not trust `.hoex.yaml` blindly:

- The host keeps the last approved content of `.hoex.yaml` outside the project, in
  `~/.local/state/hoex/approved/` (or `$XDG_STATE_HOME/hoex/approved/`). On the
  first start in a project, the current file counts as approved.
- When the file differs from the approved content, at startup or while running,
  the host shows the difference rule by rule in its terminal UI and waits for your
  decision: `a` approves, `r` rejects. Until then the approved rules stay in effect.
- An invalid new file is reported, and the approved rules stay in effect.
- With `--no-tui` there is no way to approve; start the host with the terminal UI.

Changes to the user-level file are not watched; they are read at startup and
together with the next project file change.

## The host

`hoex --mode host` runs in the foreground in the project root until you quit with
`q` or Ctrl+C. Commands run in the project directory, or in the sandbox's current
directory if that is inside the project, with the environment of the host daemon.
The sandbox cannot set environment variables.

The terminal UI lists the last runs in a 10-line panel at the top (● running,
✓ exit 0, ✗ failed, ⊘ rejected, ■ aborted) and the output of the selected run
below it, stderr in red.

| Key | Action |
| --- | --- |
| `J` / `K` (Shift+J / Shift+K) | Focus the output / the run list |
| `j` / `k`, ↓ / ↑ | Run list: select a run (stops following new runs); output: scroll by a line |
| `h` / `l`, ← / → | Run list: scroll the selected row sideways; output: scroll sideways |
| `f`, End | Follow the latest run |
| PgUp / PgDn, Ctrl+U / Ctrl+D | Scroll the output |
| `a` / `r` | Approve / reject a config change |
| `q`, Ctrl+C | Quit (stops the host) |

With `--no-tui`, or when stdout is not a terminal, the host prints plain log lines
instead.

**Audit log.** Every start and stop, run, rejection and config decision is
appended to `.hoex/hoex.log`, one JSON object per line. Command output is not
logged. The log stays after the host stops; only `.hoex/connection` is removed.
The `.hoex/` directory has its own `.gitignore`.

## In the sandbox

```bash
hoex --mode sandbox -- <command> [arguments...]
```

Everything after `--` is sent as is, as a list of arguments. Do redirection,
pipes and variable expansion in the sandbox shell around hoex:

```bash
hoex --mode sandbox -- flutter build apk > build.log 2>&1
hoex --mode sandbox -- flutter test | grep FAIL
```

The command gets no input (stdin is empty), so use non-interactive flags. Within
stdout and within stderr the order is kept; between the two it is not guaranteed.

To reach a host other than `host.docker.internal`, set `HOEX_HOST`.

**Exit codes.** hoex exits with the command's own exit code, or 128+N if the
command was killed by signal N. Its own errors start with `hoex:` on stderr:

| Code | Meaning |
| --- | --- |
| 64 | Usage error, e.g. no command after `--` |
| 69 | Host unreachable: the host is not running in this project, the port is not allowed in the sandbox network policy, or the token is wrong (another project's host on the same port) |
| 70 | Internal or protocol error |
| 77 | Rejected by the host: the command is not allowed, or the directory is outside the project |

A command can use these codes too; the `hoex:` message tells them apart.

## The Claude Code skill

`hoex --skill claude` writes, in the project:

- `.claude/skills/hoex/SKILL.md`: the skill. It lists the allowed commands with
  their descriptions and examples, explains how to call hoex, what having no shell
  on the host means, and what the exit codes mean. Its description names the
  commands, so Claude Code uses it whenever it is about to run one of them.
- `.claude/settings.json`: adds `Read(/.hoex.yaml)` and `Edit(/.hoex.yaml)` to
  `permissions.deny`, keeping the rest of the file.

The skill describes the approved rules. The host regenerates an installed skill at
startup and after every approved config change, so it stays in sync. The skill is
in English; your rule descriptions are copied as written.

## Security model

- The host runs only commands that match a rule. Each request passes three checks:
  the token, `deny_chaining`, and the allow list.
- Commands run without a shell, so arguments cannot inject commands.
- The host listens on `127.0.0.1` only, with a new token for every start. The
  sandbox network policy must allow the port.
- Config changes take effect only after you approve them on the host (see above).
  Claude Code is also told not to read or edit `.hoex.yaml`, but the approval is
  what protects you: a process in the sandbox can write the file anyway.
- The host only writes inside the project (`.hoex/`, the skill, the Claude Code
  settings) and does not follow symbolic links that lead outside it, since the
  sandbox could plant them.
- Every run is visible in the host terminal and recorded in the audit log.

**Known limitation: commands that run project files.** If an allowed command
executes files from the project (a shell script, `npm run`, `make`, build scripts,
test code), the agent can change those files in the sandbox, and the host will run
the changed version. The allow list then only limits which entry point is used, not
what runs. Allow such commands only if you are comfortable with that. The skill
tells the agent not to do this, but that is not an enforcement.

**Known limitation: the sandbox may see host changes late.** The project directory
is shared between the host and the sandbox (in Docker Sandboxes on Linux, through
virtiofs). Changes made by a command on the host can show up in the sandbox late or
not at all for a while: a directory listing may miss files the command created, and
a file the host keeps appending to (such as `.hoex/hoex.log`) may look older or
shorter than it is. New directories and files opened by their exact path were
usually up to date in our tests. This is the guest-side cache of the virtiofs share
inside the sandbox VM, not hoex; the output and exit code that hoex returns are
always complete.

Dropping the sandbox VM's caches makes the current host state visible at once.
In the default sandbox image the `agent` user has passwordless `sudo`:

```bash
sudo sh -c 'sync; echo 3 > /proc/sys/vm/drop_caches'
```

This only empties caches (the next file accesses are a little slower); no data is
lost. The skill tells the agent to rely on the hoex output and exit code, and to
drop the caches when a file a host command wrote looks missing or outdated.

## Command line

```
hoex --mode host [--port N] [--no-tui]  run the host daemon in the project root
hoex --mode sandbox -- <command>...     run a command on the host from the sandbox
hoex --skill claude                     install the agent skill into the project
hoex --version                          print the version
```

## Development

Requires Go (see `go.mod`). For the protocol: [buf](https://buf.build),
`protoc-gen-go` and `protoc-gen-go-grpc`.

```bash
make build      # bin/hoex
make test       # go test -race ./... (make test RACE= without a C compiler)
make lint       # golangci-lint and buf lint
make proto      # regenerate gen/ from proto/
```

Releases are built by [GoReleaser](https://goreleaser.com) when a `v*` tag is
pushed (`.github/workflows/release.yml`). To try the release build locally:
`goreleaser release --snapshot --clean`.

## License

[MIT](LICENSE)
