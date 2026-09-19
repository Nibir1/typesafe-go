package main

import (
	"context"
	"fmt"
	"strings"
)

// cmdCompletion prints a shell completion script.
//
// Written by hand rather than generated, because the generators come with the
// flag libraries this CLI deliberately does not use. Three shells, one command
// list, no dependency.
func cmdCompletion(ctx context.Context, args []string) int {
	fs := newFlagSet("completion <bash|zsh|fish>", "Print a shell completion script.", `  # bash
  typesafe completion bash > /usr/local/etc/bash_completion.d/typesafe

  # zsh
  typesafe completion zsh > "${fpath[1]}/_typesafe"

  # fish
  typesafe completion fish > ~/.config/fish/completions/typesafe.fish`)
	if code, ok := parse(fs, args); !ok {
		return code
	}
	if fs.NArg() != 1 {
		errorf("specify one of: bash, zsh, fish")
		fs.Usage()
		return exitUsage
	}

	switch fs.Arg(0) {
	case "bash":
		fmt.Print(bashCompletion())
	case "zsh":
		fmt.Print(zshCompletion())
	case "fish":
		fmt.Print(fishCompletion())
	default:
		errorf("unknown shell %q: use bash, zsh, or fish", fs.Arg(0))
		return exitUsage
	}
	return exitOK
}

func commandNames() []string {
	cs := commands()
	out := make([]string, 0, len(cs))
	for _, c := range cs {
		out = append(out, c.name)
	}
	return out
}

func bashCompletion() string {
	return fmt.Sprintf(`# bash completion for typesafe
_typesafe() {
  local cur prev
  cur="${COMP_WORDS[COMP_CWORD]}"
  prev="${COMP_WORDS[COMP_CWORD-1]}"

  if [ "$COMP_CWORD" -eq 1 ]; then
    COMPREPLY=( $(compgen -W "%s" -- "$cur") )
    return
  fi

  case "$prev" in
    -f|--state|--policy|--answers|--cassette|--out)
      COMPREPLY=( $(compgen -f -- "$cur") ); return ;;
    --format)
      COMPREPLY=( $(compgen -W "table json" -- "$cur") ); return ;;
  esac

  case "${COMP_WORDS[1]}" in
    completion) COMPREPLY=( $(compgen -W "bash zsh fish" -- "$cur") ) ;;
    *)          COMPREPLY=( $(compgen -W "-f --state --state-text --model --format -h" -- "$cur") ) ;;
  esac
}
complete -F _typesafe typesafe
`, strings.Join(commandNames(), " "))
}

func zshCompletion() string {
	var b strings.Builder
	b.WriteString("#compdef typesafe\n\n_typesafe() {\n  local -a commands\n  commands=(\n")
	for _, c := range commands() {
		// Escape the colon that separates a zsh completion's value from its
		// description, so a summary containing one does not truncate it.
		fmt.Fprintf(&b, "    '%s:%s'\n", c.name, strings.ReplaceAll(c.summary, ":", "\\:"))
	}
	b.WriteString(`  )

  _arguments -C \
    '1: :->command' \
    '*: :->args'

  case $state in
    command) _describe 'command' commands ;;
    args)
      case $words[2] in
        completion) _values 'shell' bash zsh fish ;;
        *)          _files ;;
      esac
      ;;
  esac
}

_typesafe "$@"
`)
	return b.String()
}

func fishCompletion() string {
	var b strings.Builder
	b.WriteString("# fish completion for typesafe\n")
	b.WriteString("complete -c typesafe -f\n")
	for _, c := range commands() {
		fmt.Fprintf(&b, "complete -c typesafe -n __fish_use_subcommand -a %s -d %q\n", c.name, c.summary)
	}
	b.WriteString(`
complete -c typesafe -s f -r -d 'request JSON file'
complete -c typesafe -l state -r -d 'state file'
complete -c typesafe -l cassette -r -d 'cassette file'
complete -c typesafe -l policy -r -d 'policy JSON file'
complete -c typesafe -l format -x -a 'table json' -d 'output format'
complete -c typesafe -n '__fish_seen_subcommand_from completion' -a 'bash zsh fish'
`)
	return b.String()
}
