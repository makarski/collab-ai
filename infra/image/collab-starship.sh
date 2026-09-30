# Initialize once per interactive Bash shell, including SSH and tmux.
case $- in
  *i*)
    if [ -n "${BASH_VERSION:-}" ] && [ "$(id -u)" = 1001 ] &&
       [ -z "${_COLLAB_STARSHIP_INITIALIZED:-}" ]; then
      eval "$(/usr/local/bin/starship init bash)"
      _COLLAB_STARSHIP_INITIALIZED=1
    fi
    ;;
esac
