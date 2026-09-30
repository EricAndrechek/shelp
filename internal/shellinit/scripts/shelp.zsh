# shelp shell integration for zsh. Load with:  eval "$(shelp init zsh)"
# `shelp <task>` puts the suggested command on your next prompt line
# (print -z), ready to edit or run. Other subcommands pass straight through.
shelp() {
  case "${1-}" in
    ''|-h|--help|help|explain|-e|init|setup|providers|version|--version)
      SHELP_SHELL=zsh command shelp "$@" ;;
    *)
      local cmd
      cmd=$(SHELP_SHELL=zsh command shelp suggest "$@") || return
      [[ -n $cmd ]] && print -z -- "$cmd" ;;
  esac
}

# Widget: turn the text on the command line into a command in place.
# Bind it yourself, e.g.:  bindkey '^G' shelp-widget
shelp-widget() {
  [[ -z $BUFFER ]] && return
  local cmd
  zle -I
  print -rn -- $'\e[2m…\e[0m' >&2
  cmd=$(SHELP_SHELL=zsh command shelp suggest -- "$BUFFER" 2>/dev/null)
  print -rn -- $'\r\e[K' >&2
  [[ -n $cmd ]] && BUFFER=$cmd && CURSOR=${#BUFFER}
  zle reset-prompt
}
zle -N shelp-widget
