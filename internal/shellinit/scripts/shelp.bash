# shelp shell integration for bash. Load with:  eval "$(shelp init bash)"
# `shelp <task>` shows an editable "run>" prompt prefilled with the
# suggested command: Enter runs it, Ctrl-C abandons. It is also pushed to
# history so ↑ recalls it. Non-interactive: just prints the command.
shelp() {
  case "${1-}" in
    ''|-h|--help|help|explain|-e|init|setup|providers|version|--version)
      SHELP_SHELL=bash command shelp "$@" ;;
    *)
      local cmd
      cmd=$(SHELP_SHELL=bash command shelp suggest "$@") || return
      [ -n "$cmd" ] || return 1
      if [ -t 0 ]; then
        history -s "$cmd" 2>/dev/null
        if read -r -e -p "run> " -i "$cmd" cmd && [ -n "$cmd" ]; then
          history -s "$cmd" 2>/dev/null
          eval "$cmd"
        fi
      else
        printf '%s\n' "$cmd"
      fi ;;
  esac
}

# Widget: turn the text on the command line into a command in place.
# Bind it yourself, e.g.:  bind -x '"\C-g": shelp-widget'
shelp-widget() {
  [ -n "$READLINE_LINE" ] || return
  local cmd
  cmd=$(SHELP_SHELL=bash command shelp suggest -- "$READLINE_LINE" 2>/dev/null) || return
  [ -n "$cmd" ] && READLINE_LINE=$cmd && READLINE_POINT=${#cmd}
}
