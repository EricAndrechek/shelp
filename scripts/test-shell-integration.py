#!/usr/bin/env python3
"""Interactive pty tests for shelp's shell integration: raw line mode and
the --bind keybinding option (see CLAUDE.md's shell-v2 plan, item 4).

Drives zsh and bash directly via a pseudo-terminal. fish runs natively
when a `fish` is on PATH (CI: `brew install fish` / `apt-get install
fish`), otherwise inside a throwaway Docker container (`docker run --rm
...`, fish from Debian, a linux build of shelp); both run the same checks.
SHELP_TEST_FISH=docker forces the container, SHELP_TEST_FISH=native
requires a fish on PATH, and SHELP_TEST_FISH=/path/to/fish picks one.

Every session runs with its own temp HOME (and ZDOTDIR/XDG_* dirs as
needed) and a minimal PATH pointed at a *stub* `shelp` binary -- never the
user's real shell config. The stub delegates `init`/`version` to the real
built binary (so the actual generated scripts are under test) but answers
`suggest`/`explain` deterministically from its own arguments (a shell
command that echoes the exact raw text it received), so a test can assert
byte-for-byte what the shell integration sent -- no model, no network.

Usage:
  python3 scripts/test-shell-integration.py [--shell zsh|bash|fish|all] [-v]

Exit status is nonzero if any case failed. Builds shelp itself (`go
build`, plus a linux cross-build for the Docker fish path), so it needs
Go; zsh and a bash >= 4 (macOS: Homebrew's) must be installed, and fish
or Docker. Works on macOS and Linux.

The screen checks render each session's byte stream with pyte (a VT100
emulator) to see what is actually left on screen. pyte is a dev-only
dependency: when it isn't importable and `uv` is on PATH, the suite re-runs
itself under `uv run --with pyte`; with neither, those checks fall back to
weaker byte-stream assertions (see explain_screen_check). The pty also
answers the terminal queries fish 4 sends, as a terminal would (see
Session._answer).
"""
from __future__ import annotations

import argparse
import fcntl
import os
import pty
import re
import select
import shlex
import shutil
import struct
import subprocess
import sys
import tempfile
import termios
import textwrap
import time

try:
    import pyte
except ImportError:
    pyte = None
    if shutil.which("uv") and not os.environ.get("SHELP_TEST_UV_REEXEC"):
        _uv = ["uv", "run", "--quiet", "--no-project", "--with", "pyte", "python3"]
        if subprocess.run(_uv + ["-c", "import pyte"], capture_output=True).returncode == 0:
            os.environ["SHELP_TEST_UV_REEXEC"] = "1"
            os.execvp("uv", _uv + [os.path.abspath(__file__)] + sys.argv[1:])

ANSI_RE = re.compile(
    r'\x1b\][^\x07\x1b]*(\x07|\x1b\\)'  # OSC (e.g. window-title setting), terminated by BEL or ST
    r'|\x1b\[[0-9;?]*[a-zA-Z]'           # CSI (colors, cursor movement, erase, bracketed-paste toggle...)
    r'|\x1b[()][A-Za-z0-9]'              # charset selection (ESC ( B, ...)
    r'|\x1b[=>]'                         # keypad mode
    r'|\x08'                             # backspace
)


def strip_ansi(s):
    return ANSI_RE.sub('', s)


# terminal queries Session answers: DA1 and CPR
QUERY_RE = re.compile(rb"\x1b\[0?c|\x1b\[6n")
# a complete DCS string (ESC P ... ST), which pyte can't parse
DCS_RE = re.compile(rb"\x1bP.*?\x1b\\", re.S)

REPO = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
VERBOSE = False
# Every Session's window size (see Session).
ROWS, COLS = 40, 120


def vprint(*a):
    if VERBOSE:
        print(*a, file=sys.stderr)


# --------------------------------------------------------------------
# PTY driver
# --------------------------------------------------------------------

class Session:
    """A shell (or `docker run` client) attached to a pty.

    ctty=True makes the pty the child's controlling terminal (setsid +
    TIOCSCTTY), the way a terminal emulator starts a shell. Without it the
    shell has no controlling tty, so ^C reaches it as a plain byte instead
    of SIGINT -- fine for most cases, but a Ctrl-C test then never
    interrupts anything for real.

    The pty gets a fixed ROWS x COLS window, so the shell lays out its
    output for the same screen a test renders it on (screen()). raw keeps
    every byte read, never reset (unlike buf)."""

    def __init__(self, argv, env, cwd=None, ctty=False):
        self.master_fd, slave_fd = pty.openpty()
        fcntl.ioctl(slave_fd, termios.TIOCSWINSZ, struct.pack("HHHH", ROWS, COLS, 0, 0))
        self.raw = bytearray()
        self._screen = self._stream = None
        self._fed = 0
        self._qpos = 0
        preexec = None
        if ctty:
            def preexec():
                fcntl.ioctl(0, termios.TIOCSCTTY, 0)
        self.proc = subprocess.Popen(
            argv, stdin=slave_fd, stdout=slave_fd, stderr=slave_fd,
            env=env, cwd=cwd, start_new_session=True, close_fds=True,
            preexec_fn=preexec,
        )
        os.close(slave_fd)
        self.buf = ""

    def read_available(self, timeout=0.3):
        """Drain whatever arrives within timeout, extending briefly while
        output keeps flowing. Always use this (never a blind sleep) so the
        pty's output buffer doesn't back up and stall the child."""
        end = time.time() + timeout
        got = False
        while time.time() < end:
            try:
                r, _, _ = select.select([self.master_fd], [], [], max(0, end - time.time()))
            except (OSError, ValueError):
                break
            if self.master_fd in r:
                try:
                    data = os.read(self.master_fd, 65536)
                except OSError:
                    break
                if not data:
                    break
                self._got(data)
                got = True
                end = time.time() + 0.1
            elif got:
                break
        return got

    def _got(self, data):
        self.raw += data
        self.buf += data.decode("utf-8", "replace")
        self._answer()

    def _answer(self):
        """Answer terminal queries the way a terminal emulator would. fish
        4 sends Primary Device Attributes (DA1, ESC[c / ESC[0c) and waits
        up to 10s for the reply -- then prints a warning and stops waiting
        for any -- and asks for the cursor position (CPR, ESC[6n) at every
        prompt. DA1 gets a VT220-ish reply; CPR the cursor as pyte sees it
        right after the query (no reply without pyte, like a terminal
        without CPR). Everything else (kitty keyboard, XTVERSION, OSC 11,
        XTGETTCAP) goes unanswered: those are optional, and DA1's reply
        tells fish none is coming."""
        while True:
            m = QUERY_RE.search(self.raw, self._qpos)
            if not m:
                # a query split across reads is searched again next time
                self._qpos = max(self._qpos, len(self.raw) - 3)
                return
            self._qpos = m.end()
            if m.group(0) == b"\x1b[6n":
                if not self._feed(m.end()):
                    continue
                c = self._screen.cursor
                reply = b"\x1b[%d;%dR" % (c.y + 1, c.x + 1)
            else:
                reply = b"\x1b[?62;22c"
            os.write(self.master_fd, reply)

    def _feed(self, upto):
        """Feed pyte the bytes read so far, up to offset upto. pyte has no
        DCS (ESC P ... ESC \\) parser and would print one's body as text
        (fish 4's XTGETTCAP queries), so complete ones are dropped and an
        unterminated one is held back until it ends."""
        if pyte is None:
            return False
        if self._screen is None:
            self._screen = pyte.Screen(COLS, ROWS)
            self._stream = pyte.ByteStream(self._screen)
        chunk = bytes(self.raw[self._fed:upto])
        start = chunk.rfind(b"\x1bP")
        if start >= 0 and chunk.find(b"\x1b\\", start) < 0:
            chunk = chunk[:start]
        self._fed += len(chunk)
        self._stream.feed(DCS_RE.sub(b"", chunk))
        return True

    def read_for(self, seconds):
        """Drain output for exactly `seconds`. Unlike read_available, a
        steady stream (the zsh spinner redraws every 80ms) can't stretch
        it -- use this to act at a given moment while output keeps
        flowing."""
        end = time.time() + seconds
        while True:
            left = end - time.time()
            if left <= 0:
                return
            try:
                r, _, _ = select.select([self.master_fd], [], [], left)
            except (OSError, ValueError):
                return
            if self.master_fd in r:
                try:
                    data = os.read(self.master_fd, 65536)
                except OSError:
                    return
                if not data:
                    return
                self._got(data)

    def screen(self):
        """The screen as the terminal would show it now (pyte), as a list
        of right-stripped rows; None without pyte. Fed incrementally."""
        if not self._feed(len(self.raw)):
            return None
        return [row.rstrip() for row in self._screen.display]

    def cursor(self):
        """(row, col) of the cursor as of the last screen() call."""
        return (self._screen.cursor.y, self._screen.cursor.x) if self._screen else None

    def cursor_visible(self):
        return self._screen is not None and not self._screen.cursor.hidden

    def send(self, s):
        os.write(self.master_fd, s.encode())

    def expect(self, pattern, timeout=8.0, bounded=False):
        """Wait for pattern (matched against the ANSI-stripped buffer).
        bounded=True never reads past timeout even while output keeps
        flowing (see read_for)."""
        end = time.time() + timeout
        rx = re.compile(pattern) if isinstance(pattern, str) else pattern
        while time.time() < end:
            if rx.search(strip_ansi(self.buf)):
                return True
            step = min(0.2, max(0.01, end - time.time()))
            if bounded:
                self.read_for(min(0.05, step))
            else:
                self.read_available(timeout=step)
        return bool(rx.search(strip_ansi(self.buf)))

    def close(self):
        try:
            self.send("\x03")
            self.read_available(0.2)
            self.send("exit\r")
        except Exception:
            pass
        try:
            self.proc.wait(timeout=3)
        except Exception:
            try:
                self.proc.terminate()
                self.proc.wait(timeout=2)
            except Exception:
                try:
                    self.proc.kill()
                except Exception:
                    pass
        try:
            os.close(self.master_fd)
        except Exception:
            pass


# --------------------------------------------------------------------
# Test bookkeeping
# --------------------------------------------------------------------

class Results:
    def __init__(self, shell):
        self.shell = shell
        self.cases = []  # (name, ok, detail)

    def check(self, name, ok, detail=""):
        self.cases.append((name, ok, detail))
        mark = "PASS" if ok else "FAIL"
        line = f"  [{mark}] {self.shell}: {name}"
        if not ok and detail:
            line += f" -- {detail}"
        print(line)
        return ok

    @property
    def failed(self):
        return [c for c in self.cases if not c[1]]


# --------------------------------------------------------------------
# Stub shelp + real binary
# --------------------------------------------------------------------

STUB_SOURCE = textwrap.dedent(
    '''\
    #!/usr/bin/env python3
    """Deterministic stub for `shelp`. init/version/providers/setup delegate
    to the real built binary (SHELP_REAL_BIN), with PATH pointed at nothing
    so `shelp providers` never finds -- let alone runs -- `shortcuts`,
    `osascript`, `fm` or an agent CLI (it still probes the localhost
    OpenAI-compatible ports, which is harmless). suggest/explain never
    touch a real model: they answer purely from their own arguments, so a
    test can assert the exact raw text the shell integration sent.

    Test knobs (env): SHELP_STUB_SLEEP=<seconds> sleeps first;
    SHELP_STUB_PIDFILE=<path> gets this process's pid at start;
    SHELP_STUB_MARKER=<path> is created after the sleep (so a killed stub
    never creates it); SHELP_STUB_FAIL makes it fail."""
    import os, shlex, sys, time

    args = sys.argv[1:]
    real = os.environ.get("SHELP_REAL_BIN")

    def delegate():
        env = dict(os.environ)
        env["PATH"] = "/nonexistent-shelp-test-path"
        os.execve(real, [real] + args, env)

    if not args or args[0] not in ("suggest", "explain"):
        delegate()

    mode = args[0]
    rest = args[1:]
    raw = None
    i = 0
    while i < len(rest):
        if rest[i] == "--":
            raw = rest[i + 1] if i + 1 < len(rest) else ""
            break
        i += 1
    if raw is None:
        raw = " ".join(rest)

    pidfile = os.environ.get("SHELP_STUB_PIDFILE")
    if pidfile:
        with open(pidfile, "w") as f:
            f.write(str(os.getpid()))

    sleep_s = os.environ.get("SHELP_STUB_SLEEP")
    if sleep_s:
        try:
            time.sleep(float(sleep_s))
        except KeyboardInterrupt:
            sys.exit(130)

    marker = os.environ.get("SHELP_STUB_MARKER")
    if marker:
        open(marker, "w").close()

    if os.environ.get("SHELP_STUB_FAIL"):
        sys.stderr.write("shelp: stub forced failure\\n")
        sys.exit(1)

    if mode == "suggest":
        # printf, not echo: zsh's (and some bash builds') `echo` interprets
        # backslash escapes in its argument by default, which would corrupt
        # a raw task containing literal backslashes even though it's
        # single-quoted (quoting protects against the SHELL reinterpreting
        # it, not against the echo builtin's own escape processing).
        print("printf '%s\\\\n' " + shlex.quote(raw))
    else:
        # Shaped like the real explain on a terminal: a spinner on stderr
        # that clears itself, then a header, a blank line and the
        # explanation, ending in a newline (no trailing blank line).
        if sys.stderr.isatty():
            for f in "|/-\\\\":
                sys.stderr.write("\\r" + f + " stub-spinner\\x1b[K")
                sys.stderr.flush()
                time.sleep(0.02)
            sys.stderr.write("\\r\\x1b[K")
            sys.stderr.flush()
        print("SHELP_EXPLAIN:" + raw)
        print()
        for i in range(1, 7):
            print("  EXPLAIN-L%d: line %d of 6 of the stub explanation" % (i, i))
    sys.exit(0)
    '''
)


def build_real_binary(dest, goos=None, goarch=None):
    env = dict(os.environ)
    if goos:
        # static: a container's libc may be older than this machine's
        env["GOOS"] = goos
        env["CGO_ENABLED"] = "0"
    if goarch:
        env["GOARCH"] = goarch
    subprocess.run(
        ["go", "build", "-o", dest, "./cmd/shelp"],
        cwd=REPO, env=env, check=True,
    )


def write_stub(path, python=None):
    """python: the stub's interpreter, by absolute path (default: the
    `python3` on its PATH -- what a container's stub needs). Host stubs
    use the one running this suite, so they don't depend on a python3 in
    /usr/bin or /bin, the only dirs on the shells' PATH."""
    src = STUB_SOURCE
    if python:
        src = "#!" + python + src[src.index("\n"):]
    with open(path, "w") as f:
        f.write(src)
    os.chmod(path, 0o755)


def utf8_locale():
    """A UTF-8 locale this machine has, for LANG: en_US.UTF-8, else
    C.UTF-8. The status text and spinner frames are multibyte, and zsh
    counts them in characters only under a UTF-8 locale. (Linux lists them
    as en_US.utf8/C.utf8; LANG takes either spelling.)"""
    try:
        out = subprocess.run(["locale", "-a"], capture_output=True, text=True).stdout
    except OSError:
        out = ""
    have = {l.strip().lower().replace("-", "") for l in out.splitlines()}
    for want in ("en_US.UTF-8", "C.UTF-8"):
        if want.lower().replace("-", "") in have:
            return want
    return "en_US.UTF-8"


LANG = utf8_locale()


# --------------------------------------------------------------------
# Shared raw-text case list (per "Build" item 4 in CLAUDE.md)
# --------------------------------------------------------------------

def suggest_cmd(raw):
    return "printf '%s\\n' " + shlex.quote(raw)


def raw_cases(tmpdir, text_prefix=None):
    """tmpdir is where the fixture files/dirs are actually created (a real
    path on whatever filesystem is running the test). text_prefix, when
    given, is the path embedded in the generated task TEXT instead --
    used for the fish/Docker suite, where tmpdir is a host path (bind-
    mounted into the container) but the task text must reference where
    that mount appears *inside* the container."""
    log_dir = os.path.join(tmpdir, "logs")
    os.makedirs(log_dir, exist_ok=True)
    open(os.path.join(log_dir, "app.log"), "w").close()
    space_dir = os.path.join(tmpdir, "my dir")
    os.makedirs(space_dir, exist_ok=True)
    text_space_dir = os.path.join(text_prefix, "my dir") if text_prefix else space_dir
    return [
        ("apostrophe", "what's my ip"),
        ("double quotes", 'say "hello world" please'),
        ("quoted path with spaces", f'list files in "{text_space_dir}"'),
        ("unquoted path with spaces", f"list files in {text_space_dir}/"),
        ("glob with match present", "compress all *.log files"),
        ("dollar HOME literal", "print $HOME please"),
        ("backslashes", r"escape this: C:\Users\eric\thing"),
        ("pipe", 'test | echo "test"'),
        ("and-and", 'print hi && echo "hi"'),
        ("semicolon", "list files; then count them"),
        ("redirect", "save the date > today.txt"),
        ("command substitution", "show $(whoami) and `date`"),
        ("trailing ampersand", "run sleep 5 &"),
        ("hash comment", "find big files # in my home dir"),
    ]


# --------------------------------------------------------------------
# Screen checks (every shell)
# --------------------------------------------------------------------

# the stub's explanation rows (see STUB_SOURCE)
EXPLAIN_ROWS = ["  EXPLAIN-L%d: line %d of 6 of the stub explanation" % (i, i) for i in range(1, 7)]
# vertical cursor motion: newline, CUD/CNL (down), CUU/CPL (up), reverse
# index (up), CUP (absolute: unknown)
VMOVE = re.compile(rb"\n|\x1b\[([\d;]*)([ABEFH])|\x1bM")


def net_rows(b):
    """Net rows the cursor moves down over the bytes b (None if they
    position it absolutely). For the no-pyte fallback only."""
    n = 0
    for m in VMOVE.finditer(b):
        if m.group(0) == b"\n":
            n += 1
        elif m.group(0) == b"\x1bM":
            n -= 1
        elif m.group(2) == b"H":
            return None
        else:
            k = int(m.group(1).split(b";")[0] or b"1")
            n += k if m.group(2) in (b"B", b"E") else -k
    return n


def last_row(rows, text):
    """Index of the last screen row equal to text, or None."""
    hits = [i for i, r in enumerate(rows) if r == text]
    return hits[-1] if hits else None


def explain_screen_check(s, results, label, start, after_rows):
    """What a raw-mode explain left on screen: the stub's header, blank
    line and all six explanation rows, then exactly ONE blank row, then
    rows starting with after_rows (the next prompt). start is len(s.raw)
    from before the explain line was typed.

    The bug this guards against: zsh's `zle reset-prompt` after output
    printed under `zle -I` moved up to where zle last drew its prompt and
    redrew it there, over the blank line and, with a two-line prompt, the
    explanation's last line. With pyte this reads the rendered screen;
    without it, the cursor must move down exactly two rows (net) between
    the end of the explanation and the first prompt text drawn after it:
    the explanation's own newline plus the blank line."""
    rows = s.screen()
    if rows is None:
        seg = bytes(s.raw[start:])
        end_row = EXPLAIN_ROWS[-1].encode()
        ok = all(r.encode() in seg for r in EXPLAIN_ROWS)
        results.check(f"{label}: explain leaves every line on screen (bytes)", ok, repr(seg[-400:]))
        tail = seg[seg.rfind(end_row) + len(end_row):] if ok else b""
        at = tail.find(after_rows[0].encode())
        between = tail[:at] if at >= 0 else tail
        ok = at >= 0 and net_rows(between) == 2
        results.check(f"{label}: explain then one blank line, then the prompt (bytes)", ok,
                      f"net rows={net_rows(between)} bytes={between[:300]!r}")
        return
    last = last_row(rows, EXPLAIN_ROWS[-1])
    view = "\n".join(f"  |{r}" for r in rows if r) if last is None else \
        "\n".join(f"  |{r}" for r in rows[max(0, last - 9):last + 2 + len(after_rows)])
    ok = (last is not None and last >= 7 and rows[last - 7].startswith("SHELP_EXPLAIN:")
          and rows[last - 6] == "" and rows[last - 5:last + 1] == EXPLAIN_ROWS)
    results.check(f"{label}: explain leaves every line on screen", ok, "screen:\n" + view)
    ok = (last is not None and last + 1 + len(after_rows) < len(rows) and rows[last + 1] == ""
          and all(rows[last + 2 + j].startswith(p) for j, p in enumerate(after_rows)))
    results.check(f"{label}: explain then one blank line, then the prompt", ok, "screen:\n" + view)


# --------------------------------------------------------------------
# zsh
# --------------------------------------------------------------------

def zsh_home(tmpdir, plugin_lines="", after_lines=""):
    """A throwaway ZDOTDIR whose .zshrc loads shelp's integration, with
    plugin_lines sourced before it and after_lines after it."""
    d = os.path.join(tmpdir, "zsh-home")
    os.makedirs(d, exist_ok=True)
    with open(os.path.join(d, ".zshrc"), "w") as f:
        f.write("PS1='TESTPROMPT%% '\n")
        f.write("HISTFILE=%s/.zsh_history\n" % d)
        f.write("HISTSIZE=200\nSAVEHIST=200\n")
        f.write(plugin_lines + "\n")
        f.write('eval "$(command shelp init zsh)"\n')
        f.write(after_lines + "\n")
        # test-only widgets: ^Y seeds a foreign region_highlight entry, ^T
        # reports BUFFER/region_highlight/POSTDISPLAY (used by the status
        # checks), ^X^G runs shelp-widget (what `--bind` binds). The two
        # probes are kept out of zsh-autosuggestions' reach, which would
        # otherwise blank POSTDISPLAY before ^T could report it.
        f.write("_t_seed() { region_highlight=('0 5 bold'); }\n")
        f.write("_t_rh() { zle -I; print -r -- \"BUF=<$BUFFER> RH=<${(j:|:)region_highlight}> PD=<$POSTDISPLAY>\"; }\n")
        f.write("zle -N _t_seed; zle -N _t_rh\n")
        f.write("ZSH_AUTOSUGGEST_IGNORE_WIDGETS+=(_t_seed _t_rh)\n")
        f.write("bindkey '^Y' _t_seed; bindkey '^T' _t_rh; bindkey '^X^G' shelp-widget\n")
    return d


def zsh_env(stub_dir, real_bin, home, cwd):
    return {
        "HOME": home, "ZDOTDIR": home,
        "PATH": stub_dir + ":/usr/bin:/bin",
        "SHELP_REAL_BIN": real_bin,
        "TERM": "xterm-256color", "LANG": LANG,
        "PWD": cwd,
    }


def zsh_suggest_roundtrip(s, raw):
    expected_line = suggest_cmd(raw)
    s.buf = ""
    s.send("shelp " + raw + "\r")
    got_line = s.expect(re.escape(expected_line), timeout=8)
    if not got_line:
        return False, f"suggested line not shown; buf={s.buf[-400:]!r}"
    s.buf = ""
    s.send("\r")
    got_echo = s.expect(re.escape(raw), timeout=8)
    return got_echo, ("" if got_echo else f"echoed output missing; buf={s.buf[-400:]!r}")


def zsh_explain(s):
    """Type a raw-mode explain and wait for the prompt to come back;
    returns len(s.raw) from before it (for explain_screen_check)."""
    start = len(s.raw)
    s.buf = ""
    s.send("shelp explain ls -la\r")
    s.expect(re.escape(EXPLAIN_ROWS[-1]), timeout=8)
    s.expect(r"TESTPROMPT% \r?$", timeout=8)
    s.read_available(0.3)
    return start


def zsh_explain_screen_checks(s, results, label):
    """Raw-mode explain as rendered on screen: with the one-line test
    prompt, with a two-line prompt (powerlevel10k-style, where the old
    `zle reset-prompt` drew over the explanation's last line), and when
    shelp fails (its error, a blank line, then the typed line back)."""
    start = zsh_explain(s)
    explain_screen_check(s, results, label, start, ["TESTPROMPT%"])

    s.send("PS1=$'TESTTOP\\nTESTPROMPT%% '\r")
    s.read_available(0.5)
    start = zsh_explain(s)
    explain_screen_check(s, results, f"{label} two-line prompt", start, ["TESTTOP", "TESTPROMPT%"])
    s.send("PS1='TESTPROMPT%% '\r")
    s.read_available(0.5)

    s.send("export SHELP_STUB_FAIL=1\r")
    s.read_available(0.5)
    s.buf = ""
    s.send("shelp explain ls -la\r")
    s.expect(r"TESTPROMPT% shelp explain ls -la", timeout=8)
    s.read_available(0.3)
    rows = s.screen()
    if rows is None:
        ok = "stub forced failure" in strip_ansi(s.buf)
        results.check(f"{label}: failed explain shows the error, keeps the line", ok, s.buf[-300:])
    else:
        at = last_row(rows, "shelp: stub forced failure")
        ok = (at is not None and at + 2 < len(rows) and rows[at + 1] == ""
              and rows[at + 2].startswith("TESTPROMPT% shelp explain ls -la"))
        end = max((i for i, r in enumerate(rows) if r), default=0)
        view = "\n".join(f"  |{r}" for r in rows[max(0, end - 6):end + 1])
        results.check(f"{label}: failed explain shows the error, a blank line, the line back",
                      ok, "screen:\n" + view)
    s.send("\x15")
    s.read_available(0.3)
    s.send("unset SHELP_STUB_FAIL\r")
    s.read_available(0.5)


# The zsh status spinner: `  <frame> shelp: thinking…` after the line.
BRAILLE = "⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏"
STATUS_TEXT = "shelp: thinking…"
STATUS_LEN = 4 + len(STATUS_TEXT)  # 2 spaces + frame + space + text
# an SGR right before a frame or the text: the status is colored
SGR_BEFORE_STATUS = re.compile(r"\x1b\[[0-9;]*m(?=[" + BRAILLE + r"]| " + STATUS_TEXT + ")")
# what job control would print for a background job: "[1] 12345",
# "[1]  + done ...", "terminated", ...
JOB_NOISE = re.compile(r"\[\d+\]|\b(done|terminated|killed|interrupt|running|suspended)\b")


CURSOR_HIDE = "\x1b[?25l"
CURSOR_SHOW = "\x1b[?25h"


def frames_in(raw):
    return {c for c in strip_ansi(raw) if c in BRAILLE}


def status_entries(rep, buflen):
    """The spinner's own region_highlight entries (by range) still present
    in a ^T report for a BUFFER of length buflen."""
    m = re.search(r"RH=<(.*?)> PD=<", rep)
    if not m:
        return ["<no RH in report>"]
    starts = (f"{buflen + 2} {buflen + 3} ", f"{buflen + 3} {buflen + STATUS_LEN} ")
    return [e for e in m.group(1).split("|") if e.startswith(starts)]


def zsh_report(s):
    s.buf = ""
    s.send("\x14")
    s.expect(r"PD=<[^>\r\n]*>", timeout=4)
    s.read_available(0.2)
    return strip_ansi(s.buf)


def wait_for_file(s, path, timeout):
    end = time.time() + timeout
    while time.time() < end:
        if os.path.exists(path) and os.path.getsize(path) > 0:
            return True
        s.read_for(0.05)
    return False


def pid_alive(pid):
    try:
        os.kill(pid, 0)
    except ProcessLookupError:
        return False
    except PermissionError:
        return True
    return True


def zsh_anim_checks(s, results, label, scratch):
    """The animated in-progress status: shelp runs in the background while
    the frame spins; afterwards the line holds the suggestion and the
    status and its highlights are gone (a foreign region_highlight entry
    survives); no job-control chatter; Ctrl-C kills the background shelp
    and hands the original line back; NO_COLOR keeps the spin but drops
    the color; a plain command runs normally right after."""
    os.makedirs(scratch, exist_ok=True)
    pidfile = os.path.join(scratch, "stub.pid")
    marker = os.path.join(scratch, "stub.marker")
    seen = ""   # everything printed from here on, for the job-noise check
    pids = []

    def run(line, wait=0.5):
        nonlocal seen
        s.buf = ""
        s.send(line + "\r")
        s.read_available(wait)
        seen += s.buf

    def fresh_pidfile():
        if os.path.exists(pidfile):
            os.remove(pidfile)

    def note_pid():
        try:
            with open(pidfile) as f:
                pids.append(int(f.read().strip()))
        except (OSError, ValueError):
            pass

    run("unset NO_COLOR COLORTERM SHELP_STUB_MARKER; export SHELP_STUB_SLEEP=1.5 SHELP_STUB_PIDFILE="
        + shlex.quote(pidfile))
    task = "anim probe"
    buflen = len("shelp " + task)
    modes = (
        ("256-color", "unset NO_COLOR COLORTERM", r"\x1b\[36m"),
        ("truecolor", "export COLORTERM=truecolor", r"\x1b\[38;2;0;196;180m"),
        ("NO_COLOR", "unset COLORTERM; export NO_COLOR=1", None),
    )
    for tag, setup, frame_sgr in modes:
        run(setup)
        fresh_pidfile()
        s.send("shelp " + task)
        s.read_available(0.3)
        s.send("\x19")  # seed a foreign highlight entry
        s.read_available(0.2)
        s.buf = ""
        s.send("\r")
        got = s.expect(re.escape(suggest_cmd(task)), timeout=8)
        during = s.buf
        seen += during
        note_pid()
        fr = frames_in(during)
        results.check(f"{label}: spinner animates while shelp runs ({tag})",
                      got and len(fr) >= 2 and STATUS_TEXT in during,
                      f"suggestion={got} frames={sorted(fr)} tail={during[-300:]!r}")
        # the cursor is hidden before the first frame and shown again after
        # the last one, so it never jumps onto the spinner between redraws
        hide, show = during.find(CURSOR_HIDE), during.rfind(CURSOR_SHOW)
        first = re.search("[" + BRAILLE + "]", during)
        last = max((m.end() for m in re.finditer("[" + BRAILLE + "]", during)), default=-1)
        results.check(f"{label}: cursor hidden while the spinner runs ({tag})",
                      first is not None and 0 <= hide < first.start() and show > last,
                      f"hide={hide} first={first.start() if first else None} show={show} last={last}")
        if frame_sgr is None:
            m = SGR_BEFORE_STATUS.search(during)
            results.check(f"{label}: NO_COLOR status has no color", m is None,
                          repr(during[max(0, m.start() - 60):m.end() + 20]) if m else "")
        else:
            ok = (re.search(frame_sgr + "[" + BRAILLE + "]", during) is not None
                  and re.search(r"\x1b\[90m " + STATUS_TEXT, during) is not None)
            results.check(f"{label}: frame cyan, text gray ({tag})", ok, repr(during[:400]))
        s.read_available(0.4)
        rep = zsh_report(s)
        seen += s.buf
        left = status_entries(rep, buflen)
        ok = (f"BUF=<{suggest_cmd(task)}>" in rep and "PD=<>" in rep
              and "RH=<0 5 bold" in rep and not left and "thinking" not in rep)
        results.check(f"{label}: line holds the suggestion, status cleared, foreign highlight kept ({tag})",
                      ok, f"leftover={left} rep={rep[-300:]!r}")
        s.send("\x15")  # kill the leftover suggestion line
        s.read_available(0.3)

    # shelp-widget (the --bind widget) animates the same way
    run("unset NO_COLOR COLORTERM")
    fresh_pidfile()
    s.send("widget probe")
    s.read_available(0.3)
    s.buf = ""
    s.send("\x18\x07")  # ^X^G
    got = s.expect(re.escape(suggest_cmd("widget probe")), timeout=8)
    during = s.buf
    seen += during
    note_pid()
    fr = frames_in(during)
    rep = zsh_report(s)
    seen += s.buf
    results.check(f"{label}: shelp-widget animates and fills the line",
                  got and len(fr) >= 2 and f"BUF=<{suggest_cmd('widget probe')}>" in rep
                  and "PD=<>" in rep and "thinking" not in rep,
                  f"frames={sorted(fr)} rep={rep[-300:]!r}")
    s.send("\x15")
    s.read_available(0.3)

    # failure: the line comes back and shelp's stderr shows via `zle -M`
    run("export SHELP_STUB_FAIL=1")
    fresh_pidfile()
    s.buf = ""
    s.send("shelp fail probe\r")
    got = s.expect(re.escape("shelp: shelp: stub forced failure"), timeout=8)
    seen += s.buf
    note_pid()
    s.read_available(0.3)
    rep = zsh_report(s)
    seen += s.buf
    results.check(f"{label}: failure shows shelp's error and keeps the line",
                  got and "BUF=<shelp fail probe>" in rep and "PD=<>" in rep
                  and "thinking" not in rep, f"msg={got} rep={rep[-300:]!r}")
    s.send("\x15")
    s.read_available(0.3)
    run("unset SHELP_STUB_FAIL")

    # Ctrl-C mid-run: the stub (sleep 1.5s, then create the marker) dies,
    # the typed line comes back, editable, with no status or highlight left.
    fresh_pidfile()
    if os.path.exists(marker):
        os.remove(marker)
    run("export SHELP_STUB_MARKER=" + shlex.quote(marker))
    line = "shelp what's my ip"
    s.send(line)
    s.read_available(0.3)
    s.send("\x19")
    s.read_available(0.2)
    s.buf = ""
    t0 = time.time()
    s.send("\r")
    started = (wait_for_file(s, pidfile, 5)
               and s.expect(re.escape(STATUS_TEXT), timeout=1, bounded=True))
    s.read_for(0.3)  # a few frames
    early = time.time() - t0 < 1.2  # the stub (1.5s) must still be running
    s.send("\x03")
    s.read_for(0.6)  # INT, then TERM after at most ~250ms
    s.read_available(0.2)
    after_enter = s.buf
    seen += s.buf
    note_pid()
    pid = pids[-1] if pids else None
    gone = pid is not None and not pid_alive(pid)
    results.check(f"{label}: Ctrl-C shows the cursor again", CURSOR_SHOW in after_enter,
                  after_enter[-200:])
    results.check(f"{label}: Ctrl-C kills the background shelp", started and early and gone,
                  f"started={started} early={early} pid={pid} buf={s.buf[-300:]!r}")
    rep = zsh_report(s)
    seen += s.buf
    left = status_entries(rep, len(line))
    ok = (f"BUF=<{line}>" in rep and "PD=<>" in rep and "RH=<0 5 bold" in rep
          and not left and "thinking" not in rep)
    results.check(f"{label}: Ctrl-C restores original line, status cleared",
                  ok, f"leftover={left} rep={rep[-300:]!r}")
    s.send(" please")
    s.read_available(0.3)
    rep = zsh_report(s)
    seen += s.buf
    results.check(f"{label}: line still editable after Ctrl-C",
                  f"BUF=<{line} please>" in rep, rep[-300:])
    rest = t0 + 1.5 + 1.0 - time.time()  # well past when the stub would finish
    if rest > 0:
        s.read_available(rest)
        seen += s.buf
    results.check(f"{label}: Ctrl-C'd stub never finished (no marker)",
                  not os.path.exists(marker), marker)
    results.check(f"{label}: Ctrl-C did not execute the suggestion",
                  suggest_cmd("what's my ip") not in strip_ansi(after_enter), after_enter[-300:])
    s.send("\x15")
    s.read_available(0.3)
    run("unset NO_COLOR COLORTERM SHELP_STUB_SLEEP SHELP_STUB_PIDFILE SHELP_STUB_MARKER")

    # a plain command still runs normally right after
    s.buf = ""
    s.send("echo anim-after-ok\r")
    s.expect(r"TESTPROMPT% \r?$", timeout=8)
    s.read_available(0.3)
    seen += s.buf
    lines = [l.strip("\r") for l in strip_ansi(s.buf).split("\n")]
    results.check(f"{label}: plain command runs normally right after", "anim-after-ok" in lines,
                  s.buf[-300:])

    plain = strip_ansi(seen)
    noise = JOB_NOISE.search(plain)
    stray_pid = [p for p in pids if re.search(r"\b%d\b" % p, plain)]
    results.check(f"{label}: no job-control output ([1] <pid>, done, ...)",
                  noise is None and not stray_pid and len(pids) >= 6,
                  f"match={noise.group(0) if noise else None!r} pids={pids} stray={stray_pid} "
                  f"near={plain[max(0, noise.start() - 80):noise.end() + 40]!r}" if noise else
                  f"pids={pids} stray={stray_pid}")


def run_zsh(tmpdir, results, real_bin, stub_dir, zsh_bin, with_plugins=None):
    label = "zsh" if not with_plugins else f"zsh+{with_plugins[0]}"
    cwd = os.path.join(tmpdir, "cwd-" + label.replace("/", "_"))
    os.makedirs(cwd, exist_ok=True)
    plugin_lines = with_plugins[1] if with_plugins else ""
    home = zsh_home(tmpdir + "-" + label.replace("/", "_"), plugin_lines)
    env = zsh_env(stub_dir, real_bin, home, cwd)
    s = Session([zsh_bin, "-i"], env, cwd=cwd, ctty=True)
    try:
        ok = s.expect(r"TESTPROMPT% ", timeout=10)
        if not results.check(f"{label}: prompt ready", ok, s.buf[-300:]):
            return

        for name, raw in raw_cases(cwd):
            ok, detail = zsh_suggest_roundtrip(s, raw)
            results.check(f"{label}: raw suggest -- {name}", ok, detail)

        # not executed: the FIRST Enter must not run anything (no stray
        # extra "hi"/marker output before the deliberate second Enter);
        # already implied by the roundtrip helper waiting for the
        # rewritten *display* text before sending the second Enter.

        # explain: streams + returns to an empty prompt
        s.buf = ""
        s.send("shelp explain ls -la\r")
        ok = s.expect(re.escape("SHELP_EXPLAIN:ls -la"), timeout=8)
        results.check(f"{label}: explain prints raw command", ok, s.buf[-300:])
        ok = s.expect(r"TESTPROMPT% \r?$", timeout=8)
        results.check(f"{label}: explain returns to empty prompt", ok, s.buf[-300:])
        s.read_available(0.3)
        zsh_explain_screen_checks(s, results, label)

        # history has the original typed line
        s.buf = ""
        s.send("fc -ln -80\r")
        s.expect(r"TESTPROMPT% \r?$", timeout=6)
        results.check(f"{label}: history has original apostrophe line",
                       "shelp what's my ip" in strip_ansi(s.buf), s.buf[-500:])

        # `shelp providers` (excluded subcommand) still runs normally
        s.buf = ""
        s.send("shelp providers\r")
        ok = s.expect(r"Local servers", timeout=8)
        results.check(f"{label}: shelp providers runs normally (not raw-mode)", ok, s.buf[-400:])

        # a normal command still runs (wait for the providers table to
        # fully finish printing -- probing can take a few seconds -- before
        # sending the next command)
        s.expect(r"TESTPROMPT% \r?$", timeout=15)
        s.buf = ""
        s.send("echo hi\r")
        s.expect(r"TESTPROMPT% \r?$", timeout=10)
        lines = [l.strip("\r") for l in strip_ansi(s.buf).split("\n")]
        ok = "hi" in lines
        results.check(f"{label}: plain `echo hi` still runs", ok, s.buf[-300:])

        # the animated status, Ctrl-C, NO_COLOR, job-control silence
        zsh_anim_checks(s, results, label, os.path.join(home, "anim"))
    finally:
        s.close()


def run_zsh_plugins_after(tmpdir, results, real_bin, stub_dir, zsh_bin, after_lines):
    """Plugins sourced AFTER `shelp init` (run_zsh's with_plugins sources
    them before it)."""
    label = "zsh+plugins-after"
    home = zsh_home(os.path.join(tmpdir, "plugins-after"), after_lines=after_lines)
    cwd = os.path.join(tmpdir, "cwd-plugins-after")
    os.makedirs(cwd, exist_ok=True)
    env = zsh_env(stub_dir, real_bin, home, cwd)
    s = Session([zsh_bin, "-i"], env, cwd=cwd, ctty=True)
    try:
        ok = s.expect(r"TESTPROMPT% ", timeout=10)
        if not results.check(f"{label}: prompt ready", ok, s.buf[-300:]):
            return
        ok2, detail = zsh_suggest_roundtrip(s, "what's my ip")
        results.check(f"{label}: raw suggest works", ok2, detail)
        s.read_available(0.3)
        zsh_explain_screen_checks(s, results, label)
        zsh_anim_checks(s, results, label, os.path.join(home, "anim"))
    finally:
        s.close()


# --------------------------------------------------------------------
# bash
# --------------------------------------------------------------------

def bash_binary():
    for cand in ("/opt/homebrew/bin/bash", "/usr/local/bin/bash", shutil.which("bash")):
        if cand and os.path.exists(cand):
            out = subprocess.run([cand, "--version"], capture_output=True, text=True).stdout
            m = re.search(r"version (\d+)\.", out)
            if m and int(m.group(1)) >= 4:
                return cand
    return None


def bash_home(tmpdir):
    d = os.path.join(tmpdir, "bash-home")
    os.makedirs(d, exist_ok=True)
    with open(os.path.join(d, ".bashrc"), "w") as f:
        f.write("PS1='TESTPROMPT$ '\n")
        f.write("HISTFILE=%s/.bash_history\n" % d)
        f.write("HISTSIZE=200\n")
        f.write('eval "$(command shelp init bash)"\n')
    return d


def bash_env(stub_dir, real_bin, home, cwd):
    return {
        "HOME": home,
        "PATH": stub_dir + ":/usr/bin:/bin",
        "SHELP_REAL_BIN": real_bin,
        "TERM": "xterm-256color", "LANG": LANG,
        "PWD": cwd,
    }


def bash_suggest_roundtrip(s, raw):
    expected_line = "run> " + suggest_cmd(raw)
    s.buf = ""
    s.send("shelp " + raw + "\r")
    got_line = s.expect(re.escape(expected_line), timeout=8)
    if not got_line:
        return False, f"run> line not shown; buf={s.buf[-400:]!r}"
    s.buf = ""
    s.send("\r")
    got_echo = s.expect(re.escape(raw), timeout=8)
    return got_echo, ("" if got_echo else f"echoed output missing; buf={s.buf[-400:]!r}")


def run_bash(tmpdir, results, real_bin, stub_dir, bash_bin):
    cwd = os.path.join(tmpdir, "bash-cwd")
    os.makedirs(cwd, exist_ok=True)
    home = bash_home(tmpdir)
    env = bash_env(stub_dir, real_bin, home, cwd)
    s = Session([bash_bin, "--noprofile", "-i"], env, cwd=cwd)
    try:
        ok = s.expect(r"TESTPROMPT\$ ", timeout=10)
        if not results.check("bash: prompt ready", ok, s.buf[-300:]):
            return

        for name, raw in raw_cases(cwd):
            ok, detail = bash_suggest_roundtrip(s, raw)
            results.check(f"bash: raw suggest -- {name}", ok, detail)

        # explain
        s.buf = ""
        start = len(s.raw)
        s.send("shelp explain ls -la\r")
        ok = s.expect(re.escape("SHELP_EXPLAIN:ls -la"), timeout=8)
        results.check("bash: explain prints raw command", ok, s.buf[-300:])
        ok = s.expect(r"TESTPROMPT\$ \r?$", timeout=8)
        results.check("bash: explain returns to empty prompt", ok, s.buf[-300:])
        s.read_available(0.3)
        explain_screen_check(s, results, "bash", start, ["TESTPROMPT$"])

        # history has the original typed line (see MERGE NOTES: __shelp_preaccept
        # pushes it explicitly, since bash's own auto-history doesn't record
        # the macro-submitted rewritten line)
        s.buf = ""
        s.send("history 80\r")
        s.expect(r"TESTPROMPT\$ \r?$", timeout=6)
        results.check("bash: history has original apostrophe line",
                       "shelp what's my ip" in strip_ansi(s.buf), s.buf[-600:])

        # shelp providers still runs normally
        s.buf = ""
        s.send("shelp providers\r")
        ok = s.expect(r"Local servers", timeout=8)
        results.check("bash: shelp providers runs normally (not raw-mode)", ok, s.buf[-400:])

        # plain command
        s.expect(r"TESTPROMPT\$ \r?$", timeout=15)
        s.buf = ""
        s.send("echo hi\r")
        s.expect(r"TESTPROMPT\$ \r?$", timeout=10)
        lines = [l.strip("\r") for l in strip_ansi(s.buf).split("\n")]
        ok = "hi" in lines
        results.check("bash: plain `echo hi` still runs", ok, s.buf[-300:])

        # Ctrl-C while waiting: bash has already committed the rewritten
        # line by the time the slow call happens (bind -x can't keep a
        # command line "pending" the way zsh/fish's widgets can), so the
        # best it can do is abort back to a clean prompt without ever
        # running the suggested command -- see MERGE NOTES.
        s.buf = ""
        s.send("export SHELP_STUB_SLEEP=3\r")
        s.read_available(0.5)
        s.buf = ""
        s.send("shelp what's my ip\r")
        s.read_available(0.6)
        s.send("\x03")
        ok = s.expect(r"TESTPROMPT\$ \r?$", timeout=6)
        results.check("bash: Ctrl-C returns to a clean prompt", ok, s.buf[-500:])
        no_exec = suggest_cmd("what's my ip") not in strip_ansi(s.buf)
        results.check("bash: Ctrl-C did not execute the suggestion", no_exec, s.buf[-500:])
        s.send("unset SHELP_STUB_SLEEP\r")
        s.read_available(0.5)
    finally:
        s.close()


# --------------------------------------------------------------------
# fish (native, or Docker)
# --------------------------------------------------------------------

FISH_DOCKERFILE = textwrap.dedent(
    """\
    FROM debian:bookworm-slim
    RUN apt-get update && apt-get install -y --no-install-recommends \\
        fish python3 ca-certificates && rm -rf /var/lib/apt/lists/*
    """
)

FISH_IMAGE = "shelp-fish-test:latest"


def docker_available():
    return shutil.which("docker") is not None


def docker_arch():
    """The Docker daemon's architecture as a GOARCH -- the linux shelp
    the container runs must match it -- or None when there's no daemon."""
    if not docker_available():
        return None
    try:
        out = subprocess.run(["docker", "version", "--format", "{{.Server.Arch}}"],
                             capture_output=True, text=True, timeout=60)
    except (OSError, subprocess.TimeoutExpired):
        return None
    arch = out.stdout.strip()
    return arch if out.returncode == 0 and arch else None


def find_fish():
    """Where fish runs: ("native", path), ("docker", None), or ("error",
    why). SHELP_TEST_FISH: auto (the default: native when a fish is on
    PATH, else Docker), native, docker, or the path of a fish to run."""
    want = os.environ.get("SHELP_TEST_FISH") or "auto"
    if want == "docker":
        return "docker", None
    if want in ("auto", "native"):
        path = shutil.which("fish")
        if path:
            return "native", path
        if want == "native":
            return "error", "SHELP_TEST_FISH=native, but there is no fish on PATH"
        return "docker", None
    if os.path.isfile(want) and os.access(want, os.X_OK):
        return "native", os.path.abspath(want)
    return "error", f"SHELP_TEST_FISH={want}: not an executable file"


class FishHost:
    """Runs fish for run_fish, natively or in a throwaway container, so
    both get the same checks. The session's HOME is a fresh dir on this
    machine (bind-mounted at /root in the container) and is also its cwd;
    text_prefix is that dir's path as the shell sees it."""

    def __init__(self, mode, fish, tmpdir, real_bin, stub_dir):
        self.mode, self.fish, self.tmpdir = mode, fish, tmpdir
        self.real_bin, self.stub_dir = real_bin, stub_dir
        self.text_prefix = "/root" if mode == "docker" else None

    def _docker(self, *args, **kw):
        return subprocess.run(["docker", "run", "--rm"] + list(args), **kw)

    def prepare(self):
        """(ok, detail): ready to run, and which fish it is."""
        if self.mode == "docker":
            ddir = os.path.join(self.tmpdir, "docker")
            os.makedirs(ddir, exist_ok=True)
            with open(os.path.join(ddir, "Dockerfile"), "w") as f:
                f.write(FISH_DOCKERFILE)
            # BuildKit's progress goes to stderr: shown with -v or on failure
            out = subprocess.run(["docker", "build", "-t", FISH_IMAGE, ddir],
                                 capture_output=not VERBOSE, text=True)
            if out.returncode != 0:
                return False, f"docker image build failed: {(out.stderr or '')[-800:]}"
            # the container's stub must be the container's python's
            self.stub = os.path.join(self.tmpdir, "fish-stub", "shelp")
            os.makedirs(os.path.dirname(self.stub), exist_ok=True)
            write_stub(self.stub)
            out = self._docker(FISH_IMAGE, "fish", "--version", capture_output=True, text=True)
            return out.returncode == 0, f"docker image {FISH_IMAGE}: {(out.stdout + out.stderr).strip()}"
        out = subprocess.run([self.fish, "--version"], capture_output=True, text=True)
        return out.returncode == 0, f"native {self.fish}: {(out.stdout + out.stderr).strip()}"

    def syntax_check(self):
        """`fish --no-execute` on the generated script."""
        if self.mode == "docker":
            out = self._docker("-v", f"{self.real_bin}:/usr/local/bin/shelp:ro", FISH_IMAGE, "sh", "-c",
                               "/usr/local/bin/shelp init fish > /tmp/i.fish && fish --no-execute /tmp/i.fish",
                               capture_output=True, text=True)
            return out.returncode == 0, out.stdout + out.stderr
        script = os.path.join(self.tmpdir, "init.fish")
        with open(script, "w") as f:
            f.write(subprocess.run([self.real_bin, "init", "fish"], capture_output=True, text=True,
                                   check=True).stdout)
        out = subprocess.run([self.fish, "--no-execute", script], capture_output=True, text=True,
                             env=self._env(os.path.join(self.tmpdir, "fish-syntax-home")))
        return out.returncode == 0, out.stdout + out.stderr

    def _env(self, home):
        return {
            "HOME": home,
            "XDG_CONFIG_HOME": os.path.join(home, ".config"),
            "XDG_DATA_HOME": os.path.join(home, ".local", "share"),
            "XDG_CACHE_HOME": os.path.join(home, ".cache"),
            "PATH": self.stub_dir + ":/usr/bin:/bin",
            "SHELP_REAL_BIN": self.real_bin,
            "TERM": "xterm", "LANG": LANG,
        }

    def session(self, home):
        if self.mode == "docker":
            argv = [
                "docker", "run", "--rm", "-i", "-t",
                "-v", f"{self.stub}:/usr/local/bin/shelp:ro",
                "-v", f"{self.real_bin}:/usr/local/bin/shelp-real:ro",
                "-v", f"{home}:/root", "-w", "/root",
                "-e", "HOME=/root", "-e", "TERM=xterm", "-e", "LANG=C.UTF-8",
                "-e", "SHELP_REAL_BIN=/usr/local/bin/shelp-real",
                FISH_IMAGE, "fish", "-i",
            ]
            # no ctty needed: `docker run -t` gives fish a terminal of its
            # own, which turns the ^C byte into SIGINT
            return Session(argv, dict(os.environ))
        return Session([self.fish, "-i"], self._env(home), cwd=home, ctty=True)


def fish_home(tmpdir):
    d = os.path.join(tmpdir, "fish-home")
    fishd = os.path.join(d, ".config", "fish")
    os.makedirs(fishd, exist_ok=True)
    with open(os.path.join(fishd, "config.fish"), "w") as f:
        f.write("function fish_prompt; printf 'TESTPROMPT%% '; end\n")
        f.write("function fish_greeting; end\n")
        f.write('eval (command shelp init fish | string collect)\n')
    return d


def fish_suggest_roundtrip(s, raw):
    expected_line = suggest_cmd(raw)
    s.buf = ""
    s.send("shelp " + raw + "\r")
    got_line = s.expect(re.escape(expected_line), timeout=8)
    if not got_line:
        return False, f"suggested line not shown; buf={s.buf[-400:]!r}"
    s.buf = ""
    s.send("\r")
    got_echo = s.expect(re.escape(raw), timeout=8)
    return got_echo, ("" if got_echo else f"echoed output missing; buf={s.buf[-400:]!r}")


def run_fish(tmpdir, results, host):
    ok, detail = host.prepare()
    if not results.check("fish: fish available", ok, detail):
        return
    print(f"# fish: {detail}")

    ok, detail = host.syntax_check()
    results.check("fish: `fish --no-execute` syntax check", ok, detail[-500:])

    home = fish_home(tmpdir)
    s = host.session(home)
    try:
        ok = s.expect(r"TESTPROMPT% ", timeout=25)
        if not results.check("fish: prompt ready", ok, s.buf[-400:]):
            return

        # files are created here, in HOME (the cwd); the task text names
        # them as the shell sees them
        for name, raw in raw_cases(home, text_prefix=host.text_prefix):
            ok, detail = fish_suggest_roundtrip(s, raw)
            results.check(f"fish: raw suggest -- {name}", ok, detail)

        s.buf = ""
        start = len(s.raw)
        s.send("shelp explain ls -la\r")
        ok = s.expect(re.escape("SHELP_EXPLAIN:ls -la"), timeout=8)
        results.check("fish: explain prints raw command", ok, s.buf[-300:])
        ok = s.expect(r"TESTPROMPT% \r?$", timeout=8)
        results.check("fish: explain returns to empty prompt", ok, s.buf[-300:])
        s.read_available(0.3)
        explain_screen_check(s, results, "fish", start, ["TESTPROMPT%"])

        # the same under a two-line prompt (fish redraws it with
        # `commandline -f repaint` after the output)
        s.send("function fish_prompt; printf 'TESTTOP\\nTESTPROMPT%% '; end\r")
        s.read_available(0.8)
        s.buf = ""
        start = len(s.raw)
        s.send("shelp explain ls -la\r")
        s.expect(re.escape(EXPLAIN_ROWS[-1]), timeout=8)
        s.expect(r"TESTPROMPT% \r?$", timeout=8)
        s.read_available(0.3)
        explain_screen_check(s, results, "fish two-line prompt", start, ["TESTTOP", "TESTPROMPT%"])
        s.send("function fish_prompt; printf 'TESTPROMPT%% '; end\r")
        s.read_available(0.8)

        s.buf = ""
        # piped: fish 4 pages interactive `history` output
        s.send("history search --contains -- \"shelp what's my ip\" | cat\r")
        s.expect(r"TESTPROMPT% \r?$", timeout=6)
        results.check("fish: history has original apostrophe line",
                       "shelp what's my ip" in strip_ansi(s.buf), s.buf[-500:])

        s.buf = ""
        s.send("shelp providers\r")
        ok = s.expect(r"Local servers", timeout=8)
        results.check("fish: shelp providers runs normally (not raw-mode)", ok, s.buf[-400:])

        s.expect(r"TESTPROMPT% \r?$", timeout=15)
        s.buf = ""
        s.send("echo hi\r")
        s.expect(r"TESTPROMPT% \r?$", timeout=10)
        lines = [l.strip("\r") for l in strip_ansi(s.buf).split("\n")]
        ok = "hi" in lines
        results.check("fish: plain `echo hi` still runs", ok, s.buf[-300:])

        s.buf = ""
        s.send("set -x SHELP_STUB_SLEEP 3\r")
        s.read_available(0.5)
        s.buf = ""
        s.send("shelp what's my ip\r")
        s.expect(r"shelp: thinking…", timeout=2)
        seen = "shelp: thinking…" in s.buf
        results.check("fish: status shown while running", seen, repr(s.buf[-300:]))
        head = s.buf.split("shelp: thinking…")[0]
        results.check("fish: status is colored",
                      re.search(r"\x1b\[[0-9;]*m$", head) is not None, repr(head[-80:]))
        s.send("\x03")
        ok = s.expect(re.escape("shelp what's my ip"), timeout=6)
        results.check("fish: Ctrl-C restores original line", ok, s.buf[-500:])
        no_exec = suggest_cmd("what's my ip") not in strip_ansi(s.buf).split("shelp what's my ip")[-1]
        results.check("fish: Ctrl-C did not execute the suggestion", no_exec, s.buf[-500:])

        # NO_COLOR: same status, no SGR; and it is gone once shelp returns
        s.send("\x15")
        s.send("set -x NO_COLOR 1; set -x SHELP_STUB_SLEEP 1\r")
        s.read_available(0.5)
        s.buf = ""
        s.send("shelp status probe\r")
        s.expect(r"shelp: thinking…", timeout=2)
        head = s.buf.split("shelp: thinking…")[0]
        results.check("fish: NO_COLOR status shown, uncolored",
                      "shelp: thinking…" in s.buf and "\x1b[90m" not in s.buf
                      and "\x1b[2;90m" not in s.buf, repr(s.buf[-200:]))
        s.expect(re.escape(suggest_cmd("status probe")), timeout=8)
        s.read_available(0.4)
        s.buf = ""
        s.send("\x15")
        s.read_available(0.4)
        results.check("fish: status gone after completion",
                      "thinking" not in strip_ansi(s.buf), repr(s.buf[-200:]))
    finally:
        s.close()


# --------------------------------------------------------------------
# main
# --------------------------------------------------------------------

def main():
    global VERBOSE
    ap = argparse.ArgumentParser()
    ap.add_argument("--shell", choices=["zsh", "bash", "fish", "all"], default="all")
    ap.add_argument("-v", "--verbose", action="store_true")
    args = ap.parse_args()
    VERBOSE = args.verbose

    tmproot = tempfile.mkdtemp(prefix="shelp-shelltest-")
    print(f"# working dir: {tmproot}")

    real_bin = os.path.join(tmproot, "real", "shelp")
    os.makedirs(os.path.dirname(real_bin), exist_ok=True)
    print("# building real shelp binary...")
    build_real_binary(real_bin)

    stub_dir = os.path.join(tmproot, "stub")
    os.makedirs(stub_dir, exist_ok=True)
    write_stub(os.path.join(stub_dir, "shelp"), python=sys.executable)

    results = Results("all")

    if args.shell in ("zsh", "all"):
        zsh_bin = shutil.which("zsh")
        if not zsh_bin:
            results.check("zsh: zsh found", False, "no zsh on PATH")
        else:
            run_zsh(tmproot, results, real_bin, stub_dir, zsh_bin)
            # Homebrew's zsh-autosuggestions / zsh-syntax-highlighting, if
            # installed: prove shelp-accept-line coexists with a wrapped
            # accept-line. Not Debian/Ubuntu's: their zsh-syntax-highlighting
            # 0.7.1 empties region_highlight on every redraw (0.8.0 removes
            # only its own entries, by memo=), so no other plugin's entry
            # survives it and the "foreign highlight kept" checks can't hold.
            found = []
            for name in ("zsh-autosuggestions", "zsh-syntax-highlighting"):
                for prefix in ("/opt/homebrew/share", "/usr/local/share"):
                    path = f"{prefix}/{name}/{name}.zsh"
                    if os.path.exists(path):
                        found.append(path)
                        break
            if found:
                before = "\n".join(f"source '{p}'" for p in found)
                run_zsh(tmproot, results, real_bin, stub_dir, zsh_bin,
                        with_plugins=("plugins-before", before))
                # and the reverse order: shelp init first, then the plugins
                run_zsh_plugins_after(tmproot, results, real_bin, stub_dir, zsh_bin, before)
            else:
                print("  (no Homebrew zsh-autosuggestions/zsh-syntax-highlighting found; skipping plugin-coexistence run)")

    if args.shell in ("bash", "all"):
        bbin = bash_binary()
        if bbin:
            run_bash(tmproot, results, real_bin, stub_dir, bbin)
        else:
            results.check("bash: modern bash found", False, "need bash >= 4 (macOS system /bin/bash is 3.2)")

    if args.shell in ("fish", "all"):
        mode, fish = find_fish()
        if mode == "error":
            results.check("fish: fish available", False, fish)
        elif mode == "native":
            run_fish(tmproot, results, FishHost(mode, fish, tmproot, real_bin, stub_dir))
        else:
            arch = docker_arch()
            if not arch:
                results.check("fish: fish available", False,
                              "no fish on PATH, and no Docker daemon to run it in")
            else:
                real_linux = os.path.join(tmproot, "real-linux", "shelp")
                os.makedirs(os.path.dirname(real_linux), exist_ok=True)
                print(f"# cross-compiling shelp for linux/{arch} (fish runs in Docker)...")
                try:
                    build_real_binary(real_linux, goos="linux", goarch=arch)
                except subprocess.CalledProcessError as e:
                    results.check("fish: cross-compile", False, str(e))
                else:
                    run_fish(tmproot, results, FishHost(mode, None, tmproot, real_linux, stub_dir))

    print()
    total = len(results.cases)
    failed = results.failed
    per = {}
    for name, ok, _ in results.cases:
        sh = re.match(r"(zsh|bash|fish)", name)
        n = per.setdefault(sh.group(1) if sh else "other", [0, 0])
        n[0] += ok
        n[1] += 1
    print("# " + ", ".join(f"{sh} {p}/{t}" for sh, (p, t) in per.items()))
    print(f"# {total - len(failed)}/{total} passed")
    if failed:
        print("# FAILURES:")
        for name, _, detail in failed:
            print(f"#   - {name}: {detail}")
        sys.exit(1)


if __name__ == "__main__":
    main()
