# boxt shell integration for fish.
#
# Load it from your shell startup file:
#   boxt shell-init fish | source
#
# It defines a `boxt` function that performs `boxt workspace switch <name>`
# itself, because a command cannot change its parent shell's directory. Every
# other invocation is passed through to the real binary.
function boxt
    if test "$argv[1]" = workspace; and test "$argv[2]" = switch
        if test (count $argv) -ne 3
            echo 'usage: boxt workspace switch <name>' >&2
            return 2
        end
        if test "$argv[3]" = -h; or test "$argv[3]" = --help
            command boxt workspace switch --help
            return $status
        end
        set -l dir (command boxt workspace path -- $argv[3])
        or return $status
        cd $dir
        return 0
    end
    command boxt $argv
end
