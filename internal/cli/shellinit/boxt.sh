# boxt shell integration for sh, bash and zsh.
#
# Load it from your shell startup file:
#   zsh:  eval "$(boxt shell-init zsh)"
#   bash: eval "$(boxt shell-init bash)"
#
# It defines a `boxt` function that performs `boxt workspace switch <name>`
# itself, because a command cannot change its parent shell's directory. Every
# other invocation is passed through to the real binary.
boxt() {
    if [ "${1:-}" = "workspace" ] && [ "${2:-}" = "switch" ]; then
        shift 2
        if [ "$#" -ne 1 ]; then
            printf 'usage: boxt workspace switch <name>\n' >&2
            return 2
        fi
        case "$1" in
        -h | --help)
            command boxt workspace switch --help
            return $?
            ;;
        esac
        _boxt_dir=$(command boxt workspace path -- "$1") || return $?
        cd "$_boxt_dir" || return $?
        unset _boxt_dir
        return 0
    fi
    command boxt "$@"
}
