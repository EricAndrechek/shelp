# shelp

Turn a task into a shell command, or explain one, with whatever model you
already have: Apple's on-device model, Apple Intelligence through
Shortcuts, any OpenAI-compatible API or local server, or the `claude` CLI.

```console
$ shelp what's my ip and is it behind a NAT
dig +short myip.opendns.com @resolver1.opendns.com

$ shelp explain 'lsof -nP -iTCP:8080 -sTCP:LISTEN'
$ lsof -nP -iTCP:8080 -sTCP:LISTEN

  Lists the processes listening on TCP port 8080.

  • -n — skip DNS lookups
  • -P — show port numbers, not service names
  • -iTCP:8080 — only TCP sockets on port 8080
  • -sTCP:LISTEN — only sockets in the LISTEN state

  man: lsof
```

shelp never runs anything. With the shell hook, the suggested command
lands on your prompt line, ready to edit or run.

shelp is early (0.x) and macOS-first. On Linux, the OpenAI-compatible and
`claude` providers work, as do the zsh, bash and fish hooks. The Apple
providers need a Mac, and `fm` needs macOS 27.

## Install

```sh
brew install ericandrechek/tap/shelp
# or
go install github.com/ericandrechek/shelp/cmd/shelp@latest
```

Binaries for macOS and Linux are on the
[releases page](https://github.com/EricAndrechek/shelp/releases).

Then run the guided setup. It adds the shell hook, installs the Apple
Shortcut if you want it, and picks your providers:

```sh
shelp setup
```

## Usage

```console
$ shelp find files over 100MB in my home dir    # suggest a command
$ shelp explain 'tar -xzvf a.tgz'               # explain one (or -e)
$ shelp -p fm list the 5 latest commits         # use a specific provider
$ shelp providers                               # what works on this machine
$ shelp providers -i                            # manage your providers
$ shelp history                                 # past requests and answers
$ shelp help config                             # help for one command
```

At the prompt, the text after `shelp` reaches it exactly as typed, so
quotes, globs and `$vars` need no escaping: `shelp what's my ip` works.
Ctrl-C cancels. Flags go before the text, `shelp suggest <text>` always
treats the rest as a task, and `--` ends flags.

## Providers

shelp tries the providers in your chain in order and uses the first that
answers. A provider that reports a rate limit is skipped for a while, and
when you're offline only local ones are tried.

| Provider | Runs on | Setup |
|---|---|---|
| `fm` | your Mac (Apple's on-device model) | `sudo fm license` |
| `shortcuts:apple-pro`, `shortcuts:chatgpt`, … | Apple's cloud, through one Apple Shortcut | `shelp setup shortcuts` |
| `<name>:<model>` | any OpenAI-compatible API, with your key | `shelp config add endpoint` |
| `lmstudio:<model>`, `ollama:<model>`, `llamacpp:<model>` | a local server | start the server |
| `claude:sonnet`, `claude:haiku` | the `claude` CLI, on your Claude plan | `claude /login` |

Every provider is used as a single plain completion: shelp's prompt in,
one answer out, with no tools, no file access and no agent loop. That's why
`claude` is the only AI CLI supported. [docs/agentcli.md](docs/agentcli.md)
explains how it's locked down and why Gemini, Codex, Antigravity and
Copilot CLI were dropped.

## Configuration

Settings live in `~/.config/shelp/config.toml`, and you rarely need to
edit it by hand:

```sh
shelp config                    # the file and the settings in effect
shelp config chain shortcuts:apple-pro,claude:sonnet,fm
shelp config add endpoint openrouter --preset openrouter \
  --model anthropic/claude-haiku-4.5
shelp config edit               # open it in $EDITOR
```

shelp never stores API keys: a route names the environment variable that
holds its key. If the config file is a symlink or managed by chezmoi,
shelp prints the change instead of making it.

Every setting, the provider manager's keys and the shell options are in
[docs/configuration.md](docs/configuration.md).

## History and privacy

Each request is saved to `~/.local/state/shelp/history.jsonl`, readable
only by you: what you asked, the answer, which provider answered and how
long it took. `shelp history` lists it and `-s` searches it. Turn it off
with `[history] enabled = false` or `SHELP_HISTORY=0`.

The model is told only your OS and shell unless you opt in to more.
`fm` and local servers keep everything on your machine; every other
provider receives your full request.

## Benchmark

> These numbers are outdated. They come from a 2026-09-30 run with the
> older v2 prompt, and Apple Intelligence Pro hit its usage limit early in
> that run. A new run is tracked in
> [#1](https://github.com/EricAndrechek/shelp/issues/1).

<!-- bench:start -->
| Provider | Template | Trials | Graded% | Mean score% | Fully-correct% | Format-clean% | Unavailable% | Suggest p50/p90 | Explain first-chunk p50 | Explain total p50 | Cold p50 |
|---|---|---|---|---|---|---|---|---|---|---|---|
| claude:haiku | v2 | 112 | 100% | 84% | 77% | 95% | 0% | 1976ms/2353ms | 2520ms | 2971ms | 1857ms |
| claude:sonnet | v2 | 112 | 100% | 97% | 96% | 100% | 0% | 2345ms/2983ms | 3260ms | 3729ms | 2260ms |
| fm | v2 | 112 | 100% | 36% | 29% | 100% | 0% | 586ms/1002ms | 385ms | 2392ms | - |
| shortcuts:apple-pro | v2 | 112 | 28% | 98% | 97% | 100% | 72% | 1295ms/1508ms | 1717ms | 1717ms | - |

_2026-09-30 · 81 cases · up to 1 sample(s) per trial_
<!-- bench:end -->

In short: Apple Intelligence Pro is the most accurate when it's available,
Claude Sonnet is the best fallback, and `fm` is fast and private but often
wrong. Method and details: [docs/benchmark.md](docs/benchmark.md).

## Caveats

- Apple's `fm` license limits "programmatic access" and doesn't say whether
  tools like shelp are covered. Use it at your own risk.
- The Apple Shortcut needs one click on "Add Shortcut" to import. When a
  shelp update changes it, `shelp setup shortcuts` asks you to delete the
  old one first, because Shortcuts can't replace one in place.
- Apple's cloud models have an unpublished usage limit, and `claude` uses
  your Claude plan's quota.

## Development

```sh
make build         # bin/shelp
make test          # Go tests
make test-shell    # terminal tests of the zsh, bash and fish hooks
make ci            # everything CI runs
```

## License

MIT
