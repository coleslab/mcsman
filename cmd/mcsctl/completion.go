package main

import (
	"fmt"
	"net/http"
	"strings"
)

// Command tree used by the completion scripts.
var commandTree = map[string][]string{
	"server":  {"list", "status", "create", "start", "stop", "restart", "console", "cmd", "delete"},
	"backup":  {"create", "list", "restore"},
	"invite":  {"create", "list", "revoke"},
	"plugin":  {"search", "install", "list", "update", "remove"},
	"upgrade": {"preflight", "run"},
	"world":   {"import", "export"},
	"user":    {"list", "role", "mc", "remove"},
	"member":  {"list", "add", "remove"},
}

var topLevel = []string{"init", "doctor", "version", "server", "backup", "invite", "plugin", "upgrade", "world", "user", "member", "completion", "help"}

// printServerNames is used by completion scripts; errors are silent.
func printServerNames(c *client) {
	var list []serverInfo
	if c.do(http.MethodGet, "/api/servers", nil, &list) == nil {
		for _, s := range list {
			fmt.Println(s.Name)
		}
	}
}

func cmdCompletion(shell string) error {
	var subcases strings.Builder
	for top, subs := range commandTree {
		fmt.Fprintf(&subcases, "        %s) subs=%q ;;\n", top, strings.Join(subs, " "))
	}
	switch shell {
	case "bash":
		fmt.Printf(`# mcsctl bash completion. Install:
#   mcsctl completion bash | sudo tee /usr/share/bash-completion/completions/mcsctl
_mcsctl() {
    local cur=${COMP_WORDS[COMP_CWORD]} top=${COMP_WORDS[1]} subs=""
    if [[ $COMP_CWORD -eq 1 ]]; then
        COMPREPLY=($(compgen -W %q -- "$cur")); return
    fi
    case "$top" in
%s        completion) COMPREPLY=($(compgen -W "bash zsh" -- "$cur")); return ;;
        *) return ;;
    esac
    if [[ $COMP_CWORD -eq 2 ]]; then
        COMPREPLY=($(compgen -W "$subs" -- "$cur")); return
    fi
    # Third word: a server name for most commands.
    if [[ $COMP_CWORD -eq 3 && "$top" != user && "$top" != invite && "${COMP_WORDS[2]}" != search ]]; then
        COMPREPLY=($(compgen -W "$(mcsctl __servers 2>/dev/null)" -- "$cur"))
    fi
}
complete -F _mcsctl mcsctl
`, strings.Join(topLevel, " "), subcases.String())
	case "zsh":
		fmt.Printf(`#compdef mcsctl
# mcsctl zsh completion. Install:
#   mcsctl completion zsh | sudo tee /usr/share/zsh/site-functions/_mcsctl
_mcsctl() {
    local top=${words[2]} subs=""
    if (( CURRENT == 2 )); then
        compadd -- %s; return
    fi
    case "$top" in
%s        completion) compadd -- bash zsh; return ;;
        *) return ;;
    esac
    if (( CURRENT == 3 )); then
        compadd -- ${=subs}; return
    fi
    if (( CURRENT == 4 )) && [[ "$top" != user && "$top" != invite && "${words[3]}" != search ]]; then
        compadd -- ${(f)"$(mcsctl __servers 2>/dev/null)"}
    fi
}
compdef _mcsctl mcsctl
`, strings.Join(topLevel, " "), subcases.String())
	default:
		return fmt.Errorf("usage: mcsctl completion bash|zsh")
	}
	return nil
}
