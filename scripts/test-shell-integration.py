#!/usr/bin/env python3
"""Interactive pty tests for shelp's shell integration: raw line mode and
the --bind keybinding option (see CLAUDE.md's shell-v2 plan, item 4).

Drives zsh and bash directly via a pseudo-terminal (both are installed on
this Mac); fish isn't installed here, so it's tested inside a throwaway
Docker container (`docker run --rm ...`) with fish installed from the
distro and a linux build of shelp.

Every session runs with its own temp HOME (and ZDOTDIR/XDG_CONFIG_HOME as
needed) and a minimal PATH pointed at a *stub* `shelp` binary -- never the
user's real shell config. The stub delegates `init`/`version` to the real
built binary (so the actual generated scripts are under test) but answers
`suggest`/`explain` deterministically from its own arguments (a shell
command that echoes the exact raw text it received), so a test can assert
byte-for-byte what the shell integration sent -- no model, no network.

Usage:
  python3 scripts/test-shell-integration.py [--shell zsh|bash|fish|all] [-v]

Exit status is nonzero if any case failed. Requires `make test-shell`'s
build step to have produced ./bin/shelp, or builds it itself.

The explain screen checks render each session's byte stream with pyte (a
VT100 emulator) to see what is actually left on screen. pyte is a dev-only
dependency: when it isn't importable and `uv` is on PATH, the suite re-runs
itself under `uv run --with pyte`; with neither, those checks fall back to
byte-stream assertions (see explain_screen_check).
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
                self.raw += data
                self.buf += data.decode("utf-8", "replace")
                got = True
                end = time.time() + 0.1
            elif got:
                break
        return got

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
                self.raw += data
                self.buf += data.decode("utf-8", "replace")

    def screen(self):
        """The screen as the terminal would show it now (pyte), as a list
        of right-stripped rows; None without pyte. Fed incrementally."""
        if pyte is None:
            return None
        if self._screen is None:
            self._screen = pyte.Screen(COLS, ROWS)
            self._stream = pyte.ByteStream(self._screen)
        self._stream.feed(bytes(self.raw[self._fed:]))
        self._fed = len(self.raw)
        return [row.rstrip() for row in self._screen.display]

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
        env["GOOS"] = goos
    if goarch:
        env["GOARCH"] = goarch
    subprocess.run(
        ["go", "build", "-o", dest, "./cmd/shelp"],
        cwd=REPO, env=env, check=True,
    )


def write_stub(path):
    with open(path, "w") as f:
        f.write(STUB_SOURCE)
    os.chmod(path, 0o755)


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
# Explain screen checks (every shell)
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
        "TERM": "xterm-256color", "LANG": "en_US.UTF-8",
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


def run_zsh(tmpdir, results, real_bin, stub_dir, with_plugins=None):
    label = "zsh" if not with_plugins else f"zsh+{with_plugins[0]}"
    cwd = os.path.join(tmpdir, "cwd-" + label.replace("/", "_"))
    os.makedirs(cwd, exist_ok=True)
    plugin_lines = with_plugins[1] if with_plugins else ""
    home = zsh_home(tmpdir + "-" + label.replace("/", "_"), plugin_lines)
    env = zsh_env(stub_dir, real_bin, home, cwd)
    s = Session(["/bin/zsh", "-i"], env, cwd=cwd, ctty=True)
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


def run_zsh_plugins_after(tmpdir, results, real_bin, stub_dir, after_lines):
    """Plugins sourced AFTER `shelp init` (run_zsh's with_plugins sources
    them before it)."""
    label = "zsh+plugins-after"
    home = zsh_home(os.path.join(tmpdir, "plugins-after"), after_lines=after_lines)
    cwd = os.path.join(tmpdir, "cwd-plugins-after")
    os.makedirs(cwd, exist_ok=True)
    env = zsh_env(stub_dir, real_bin, home, cwd)
    s = Session(["/bin/zsh", "-i"], env, cwd=cwd, ctty=True)
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
        "TERM": "xterm-256color", "LANG": "en_US.UTF-8",
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
# fish (Docker)
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


def build_fish_image(tmpdir):
    ddir = os.path.join(tmpdir, "docker")
    os.makedirs(ddir, exist_ok=True)
    with open(os.path.join(ddir, "Dockerfile"), "w") as f:
        f.write(FISH_DOCKERFILE)
    subprocess.run(["docker", "build", "-t", FISH_IMAGE, ddir], check=True,
                    stdout=subprocess.DEVNULL if not VERBOSE else None)


def fish_syntax_check(real_linux_bin, tmpdir):
    """`fish --no-execute` syntax check, run in the container regardless of
    whether the full interactive suite runs."""
    out = subprocess.run(
        ["docker", "run", "--rm", "-v", f"{real_linux_bin}:/usr/local/bin/shelp:ro",
         FISH_IMAGE, "sh", "-c", "/usr/local/bin/shelp init fish > /tmp/i.fish && fish --no-execute /tmp/i.fish"],
        capture_output=True, text=True,
    )
    return out.returncode == 0, out.stdout + out.stderr


def fish_home(tmpdir, container_home):
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


def run_fish(tmpdir, results, real_linux_bin):
    if not docker_available():
        results.check("fish: docker available", False, "docker not found; skipping fish interactive suite")
        return
    try:
        build_fish_image(tmpdir)
    except subprocess.CalledProcessError as e:
        results.check("fish: docker image build", False, str(e))
        return
    results.check("fish: docker image build", True)

    ok, detail = fish_syntax_check(real_linux_bin, tmpdir)
    results.check("fish: `fish --no-execute` syntax check", ok, detail[-500:])

    home = fish_home(tmpdir, "/home/tester")
    argv = [
        "docker", "run", "--rm", "-i", "-t",
        "-v", f"{real_linux_bin}:/usr/local/bin/shelp:ro",
        "-v", f"{home}:/root",
        "-e", "HOME=/root",
        "-e", "TERM=xterm",
        "-e", "SHELP_REAL_BIN=/usr/local/bin/shelp",
        "-e", "SHELP_STUB_SLEEP",
        "-e", "SHELP_STUB_FAIL",
        "--entrypoint", "fish",
        FISH_IMAGE, "-i",
    ]
    # The stub needs to be `shelp` on PATH *ahead of* the real binary
    # mount; simplest is to mount the stub itself at a PATH-first
    # location and the real binary at a fixed path the stub delegates to.
    stub_src = os.path.join(tmpdir, "fish-stub", "shelp")
    os.makedirs(os.path.dirname(stub_src), exist_ok=True)
    write_stub(stub_src)
    argv = [
        "docker", "run", "--rm", "-i", "-t",
        "-v", f"{stub_src}:/usr/local/bin/shelp:ro",
        "-v", f"{real_linux_bin}:/usr/local/bin/shelp-real:ro",
        "-v", f"{home}:/root",
        "-e", "HOME=/root",
        "-e", "TERM=xterm",
        "-e", "SHELP_REAL_BIN=/usr/local/bin/shelp-real",
        "-e", "SHELP_STUB_SLEEP",
        "-e", "SHELP_STUB_FAIL",
        FISH_IMAGE, "fish", "-i",
    ]
    env = dict(os.environ)
    env.pop("SHELP_STUB_SLEEP", None)
    env.pop("SHELP_STUB_FAIL", None)
    s = Session(argv, env)
    try:
        ok = s.expect(r"TESTPROMPT% ", timeout=25)
        if not results.check("fish: prompt ready", ok, s.buf[-400:]):
            return

        # dirs/files are created on the host (in `home`, bind-mounted at
        # /root), but the task text must reference the container path.
        cwd_cases = raw_cases(home, text_prefix="/root")
        for name, raw in cwd_cases:
            # skip the filesystem-dependent glob case (host-created files
            # aren't in the container's cwd)
            if name == "glob with match present":
                continue
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
        s.send("history search --contains -- \"shelp what's my ip\"\r")
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
    write_stub(os.path.join(stub_dir, "shelp"))

    results = Results("all")

    if args.shell in ("zsh", "all"):
        run_zsh(tmproot, results, real_bin, stub_dir)
        # Homebrew zsh-autosuggestions / zsh-syntax-highlighting, if present:
        # prove shelp-accept-line coexists with a wrapped accept-line.
        candidates = [
            ("zsh-autosuggestions",
             "/opt/homebrew/share/zsh-autosuggestions/zsh-autosuggestions.zsh"),
            ("zsh-syntax-highlighting",
             "/opt/homebrew/share/zsh-syntax-highlighting/zsh-syntax-highlighting.zsh"),
        ]
        found = [(n, p) for n, p in candidates if os.path.exists(p)]
        if found:
            before = "\n".join(f"source '{p}'" for _, p in found)
            run_zsh(tmproot, results, real_bin, stub_dir,
                    with_plugins=("plugins-before", before))
            # and the reverse order: shelp init first, then the plugins
            run_zsh_plugins_after(tmproot, results, real_bin, stub_dir, before)
        else:
            print("  (no Homebrew zsh-autosuggestions/zsh-syntax-highlighting found; skipping plugin-coexistence run)")

    if args.shell in ("bash", "all"):
        bbin = bash_binary()
        if bbin:
            run_bash(tmproot, results, real_bin, stub_dir, bbin)
        else:
            results.check("bash: modern bash found", False, "need bash >= 4 (macOS system /bin/bash is 3.2)")

    if args.shell in ("fish", "all"):
        real_linux = os.path.join(tmproot, "real-linux", "shelp")
        os.makedirs(os.path.dirname(real_linux), exist_ok=True)
        print("# cross-compiling shelp for linux/arm64...")
        try:
            build_real_binary(real_linux, goos="linux", goarch="arm64")
            run_fish(tmproot, results, real_linux)
        except subprocess.CalledProcessError as e:
            results.check("fish: cross-compile", False, str(e))

    print()
    total = len(results.cases)
    failed = results.failed
    print(f"# {total - len(failed)}/{total} passed")
    if failed:
        print("# FAILURES:")
        for name, _, detail in failed:
            print(f"#   - {name}: {detail}")
        sys.exit(1)


if __name__ == "__main__":
    main()
