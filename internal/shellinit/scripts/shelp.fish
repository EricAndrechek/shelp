# shelp shell integration for fish. Load with:  shelp init fish | source
# fish cannot pre-fill the next prompt from a function, so `shelp <task>`
# prints the command; the widget replaces the command line in place.
# Bind it yourself, e.g.:  bind \cg shelp-widget
function shelp
    switch "$argv[1]"
        case '' -h --help help explain -e init setup providers version --version
            SHELP_SHELL=fish command shelp $argv
        case '*'
            SHELP_SHELL=fish command shelp suggest $argv
    end
end

function shelp-widget
    set -l line (commandline)
    test -n "$line"; or return
    set -l cmd (SHELP_SHELL=fish command shelp suggest -- "$line" 2>/dev/null | string collect)
    test -n "$cmd"; and commandline -r -- $cmd
    commandline -f repaint
end
