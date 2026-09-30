# shelp

Turn a task into a shell command, or explain one, with whatever model you
already have.

```console
$ shelp find all files over 100MB in my home dir
$ find ~ -type f -size +100M          # lands on your prompt line, ready to edit
$ shelp explain 'lsof -nP -iTCP:8080 -sTCP:LISTEN'
```

Status: early, local-only. Benchmark results will live here.

## Install (for now)

```sh
make install                     # -> ~/.local/bin/shelp
eval "$(shelp init zsh)"         # in ~/.zshrc (bash: init bash; fish: shelp init fish | source)
shelp providers                  # what works on this machine
```
