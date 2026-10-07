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
"""
from __future__ import annotations

import argparse
import os
import pty
import re
import select
import shlex
import shutil
import subprocess
import sys
import tempfile
import textwrap
import time

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


def vprint(*a):
    if VERBOSE:
        print(*a, file=sys.stderr)


# --------------------------------------------------------------------
# PTY driver
# --------------------------------------------------------------------

class Session:
    """A shell (or `docker run` client) attached to a pty."""

    def __init__(self, argv, env, cwd=None):
        self.master_fd, slave_fd = pty.openpty()
        self.proc = subprocess.Popen(
            argv, stdin=slave_fd, stdout=slave_fd, stderr=slave_fd,
            env=env, cwd=cwd, start_new_session=True, close_fds=True,
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
                self.buf += data.decode("utf-8", "replace")
                got = True
                end = time.time() + 0.1
            elif got:
                break
        return got

    def send(self, s):
        os.write(self.master_fd, s.encode())

    def expect(self, pattern, timeout=8.0):
        end = time.time() + timeout
        rx = re.compile(pattern) if isinstance(pattern, str) else pattern
        while time.time() < end:
            if rx.search(strip_ansi(self.buf)):
                return True
            self.read_available(timeout=min(0.2, max(0.01, end - time.time())))
        return False

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
    to the real built binary (SHELP_REAL_BIN). suggest/explain never touch a
    real model: they answer purely from their own arguments, so a test can
    assert the exact raw text the shell integration sent."""
    import os, shlex, sys, time

    args = sys.argv[1:]
    real = os.environ.get("SHELP_REAL_BIN")

    def delegate():
        os.execv(real, [real] + args)

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

    sleep_s = os.environ.get("SHELP_STUB_SLEEP")
    if sleep_s:
        try:
            time.sleep(float(sleep_s))
        except KeyboardInterrupt:
            sys.exit(130)

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
        print("SHELP_EXPLAIN:" + raw)
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
# zsh
# --------------------------------------------------------------------

def zsh_home(tmpdir, plugin_lines=""):
    d = os.path.join(tmpdir, "zsh-home")
    os.makedirs(d, exist_ok=True)
    with open(os.path.join(d, ".zshrc"), "w") as f:
        f.write("PS1='TESTPROMPT%% '\n")
        f.write("HISTFILE=%s/.zsh_history\n" % d)
        f.write("HISTSIZE=200\nSAVEHIST=200\n")
        f.write(plugin_lines + "\n")
        f.write('eval "$(command shelp init zsh)"\n')
        # test-only widgets: ^Y seeds a foreign region_highlight entry, ^T
        # reports region_highlight/POSTDISPLAY (used by the status checks)
        f.write("_t_seed() { region_highlight=('0 5 bold'); }\n")
        f.write("_t_rh() { zle -I; print -r -- \"RH=<${(j:|:)region_highlight}> PD=<$POSTDISPLAY>\"; }\n")
        f.write("zle -N _t_seed; zle -N _t_rh\n")
        f.write("bindkey '^Y' _t_seed; bindkey '^T' _t_rh\n")
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


def run_zsh(tmpdir, results, real_bin, stub_dir, with_plugins=None):
    label = "zsh" if not with_plugins else f"zsh+{with_plugins[0]}"
    cwd = os.path.join(tmpdir, "cwd-" + label.replace("/", "_"))
    os.makedirs(cwd, exist_ok=True)
    plugin_lines = with_plugins[1] if with_plugins else ""
    home = zsh_home(tmpdir + "-" + label.replace("/", "_"), plugin_lines)
    env = zsh_env(stub_dir, real_bin, home, cwd)
    s = Session(["/bin/zsh", "-i"], env, cwd=cwd)
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

        # history has the original typed line
        s.buf = ""
        s.send("fc -ln -80\r")
        s.expect(r"TESTPROMPT% \r?$", timeout=6)
        results.check(f"{label}: history has original apostrophe line",
                       "shelp what's my ip" in strip_ansi(s.buf), s.buf[-500:])

        # `shelp providers` (excluded subcommand) still runs normally
        s.buf = ""
        s.send("shelp providers\r")
        ok = s.expect(r"PROVIDER", timeout=8)
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

        # Ctrl-C while waiting restores the original line
        env2 = dict(env)
        s.buf = ""
        s.send("export SHELP_STUB_SLEEP=3\r")
        s.read_available(0.5)
        s.buf = ""
        s.send("shelp what's my ip\r")
        s.read_available(0.6)  # let it get into the (3s) stub sleep before interrupting
        s.send("\x03")
        ok = s.expect(re.escape("shelp what's my ip"), timeout=6)
        results.check(f"{label}: Ctrl-C restores original line", ok, s.buf[-500:])
        # confirm it wasn't executed (no rewritten "echo ..." got submitted)
        no_exec = suggest_cmd("what's my ip") not in strip_ansi(s.buf).split("shelp what's my ip")[-1]
        results.check(f"{label}: Ctrl-C did not execute the suggestion", no_exec, s.buf[-500:])
        s.send("\x03\r")
        s.expect(r"TESTPROMPT% \r?$", timeout=6)
        s.send("unset SHELP_STUB_SLEEP\r")
        s.read_available(0.5)
        # in-progress status: gray `shelp: thinking…` while shelp runs
        # (POSTDISPLAY + region_highlight), gone afterwards, foreign
        # region_highlight entries untouched; NO_COLOR drops the color.
        s.send("export SHELP_STUB_SLEEP=2\r")
        s.read_available(0.5)
        for nc in (False, True):
            tag = "NO_COLOR" if nc else "color"
            s.send(("export NO_COLOR=1" if nc else "unset NO_COLOR") + "\r")
            s.read_available(0.5)
            s.buf = ""
            s.send("shelp status probe")
            s.read_available(0.3)
            s.send("\x19")  # seed a foreign highlight entry
            s.read_available(0.2)
            s.buf = ""
            s.send("\r")
            seen = s.expect(r"shelp: thinking…", timeout=1.5)
            during = s.buf
            results.check(f"{label}: status shown while running ({tag})", seen, during[-300:])
            if seen:
                head = during[:during.index("shelp: thinking…")]
                sgr = re.search(r"\x1b\[[0-9;]*m[^\x1b]*$", head) is not None
                results.check(f"{label}: status is colored ({tag})" if not nc else
                              f"{label}: NO_COLOR status has no color",
                              (not sgr) if nc else sgr, repr(head[-120:]))
            s.expect(re.escape(suggest_cmd("status probe")), timeout=8)
            s.read_available(0.4)
            s.buf = ""
            s.send("\x14")  # report
            s.expect(r"PD=<", timeout=4)
            rep = strip_ansi(s.buf)
            results.check(f"{label}: status cleared, region_highlight restored ({tag})",
                          ("RH=<0 5 bold" in rep and "18 36" not in rep and "thinking" not in rep), rep[-300:])
            s.send("\x15")  # kill the leftover suggestion line
            s.read_available(0.3)
        s.send("unset NO_COLOR SHELP_STUB_SLEEP\r")
        s.read_available(0.5)
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
        s.send("shelp explain ls -la\r")
        ok = s.expect(re.escape("SHELP_EXPLAIN:ls -la"), timeout=8)
        results.check("bash: explain prints raw command", ok, s.buf[-300:])
        ok = s.expect(r"TESTPROMPT\$ \r?$", timeout=8)
        results.check("bash: explain returns to empty prompt", ok, s.buf[-300:])

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
        ok = s.expect(r"PROVIDER", timeout=8)
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
        s.send("shelp explain ls -la\r")
        ok = s.expect(re.escape("SHELP_EXPLAIN:ls -la"), timeout=8)
        results.check("fish: explain prints raw command", ok, s.buf[-300:])
        ok = s.expect(r"TESTPROMPT% \r?$", timeout=8)
        results.check("fish: explain returns to empty prompt", ok, s.buf[-300:])

        s.buf = ""
        s.send("history search --contains -- \"shelp what's my ip\"\r")
        s.expect(r"TESTPROMPT% \r?$", timeout=6)
        results.check("fish: history has original apostrophe line",
                       "shelp what's my ip" in strip_ansi(s.buf), s.buf[-500:])

        s.buf = ""
        s.send("shelp providers\r")
        ok = s.expect(r"PROVIDER", timeout=8)
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
            after_lines = "\n".join(f"source '{p}'" for _, p in found)
            # "after" variant: source plugins, THEN shelp init -- but our
            # zsh_home() always appends `eval "$(shelp init zsh)"` last, so
            # passing the plugin source lines as the "before" block IS the
            # "sourced after shelp init" case when shelp init runs first;
            # test the reverse order explicitly below.
            d = os.path.join(tmproot, "zsh-home-plugins-after")
            os.makedirs(d, exist_ok=True)
            with open(os.path.join(d, ".zshrc"), "w") as f:
                f.write("PS1='TESTPROMPT%% '\n")
                f.write("HISTFILE=%s/.zsh_history\nHISTSIZE=200\nSAVEHIST=200\n" % d)
                f.write('eval "$(command shelp init zsh)"\n')
                f.write(after_lines + "\n")
            cwd = os.path.join(tmproot, "cwd-plugins-after")
            os.makedirs(cwd, exist_ok=True)
            env = zsh_env(stub_dir, real_bin, d, cwd)
            s = Session(["/bin/zsh", "-i"], env, cwd=cwd)
            try:
                ok = s.expect(r"TESTPROMPT% ", timeout=10)
                results.check("zsh+plugins-after: prompt ready", ok, s.buf[-300:])
                if ok:
                    ok2, detail = zsh_suggest_roundtrip(s, "what's my ip")
                    results.check("zsh+plugins-after: raw suggest works", ok2, detail)
            finally:
                s.close()
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
