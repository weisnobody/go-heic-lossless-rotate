package cli

// Help and usage text, laid out the way argparse's HelpFormatter does.
// docs/SPEC.md C6: this text may differ from Python's; only exit codes and
// streams must match. It does carry upstream's full module docstring, as
// Python's -h does.

import (
	"fmt"
	"strings"
)

func (e *exitError) Error() string { return fmt.Sprintf("exit %d", e.code) }

const (
	helpWidth    = 78 // argparse: terminal columns (80 when not a tty) - 2
	helpPosition = 24 // argparse's max_help_position
)

// usageArgs is the argument part of the usage line.
func (p *parser) usageArgs() string {
	var parts []string
	for _, o := range p.opts {
		name := o.short
		if name == "" {
			name = o.long
		}
		parts = append(parts, "["+name+"]")
	}
	if p.subcommands != nil {
		parts = append(parts, "{"+p.subcommandList()+"}", "...")
	}
	for _, pos := range p.positionals {
		if pos.optional {
			parts = append(parts, "["+pos.metavarOrName()+"]")
		} else {
			parts = append(parts, pos.metavarOrName())
		}
	}
	return strings.Join(parts, " ")
}

func (pos positional) metavarOrName() string {
	if pos.metavar != "" {
		return pos.metavar
	}
	return pos.name
}

// usage is argparse's "usage: prog ..." line, wrapped like argparse does.
func (p *parser) usage() string {
	prefix := "usage: " + p.prog + " "
	return wrapIndented(prefix, p.usageArgs(), len(prefix)) + "\n"
}

// formatHelp is the full -h output.
func (p *parser) formatHelp() string {
	var b strings.Builder
	b.WriteString(p.usage())
	if p.description != "" {
		b.WriteString("\n")
		if p.subcommands != nil {
			// Top level: RawDescriptionHelpFormatter - the docstring as-is.
			fmt.Fprintln(&b, strings.Trim(strings.ReplaceAll(p.description, "%(prog)s", p.prog), "\n"))
		} else {
			fmt.Fprintln(&b, wrapIndented("", p.description, 0))
		}
	}

	if p.subcommands != nil || len(p.positionals) > 0 {
		b.WriteString("\npositional arguments:\n")
		if p.subcommands != nil {
			b.WriteString(helpItem("  {"+p.subcommandList()+"}", ""))
			for _, s := range p.subcommands {
				b.WriteString(helpItem("    "+s.name, s.summary))
			}
		}
		for _, pos := range p.positionals {
			b.WriteString(helpItem("  "+pos.metavarOrName(), pos.help))
		}
	}

	b.WriteString("\noptions:\n")
	for _, o := range p.opts {
		var names []string
		for _, n := range []string{o.short, o.long} {
			if n != "" {
				names = append(names, n)
			}
		}
		b.WriteString(helpItem("  "+strings.Join(names, ", "), o.help))
	}
	return b.String()
}

// helpItem formats one "  invocation    help text" entry: the help starts at
// column helpPosition, or on the next line if the invocation is too long.
func helpItem(invocation, help string) string {
	if help == "" {
		return invocation + "\n"
	}
	indent := strings.Repeat(" ", helpPosition)
	lines := wrapWords(help, helpWidth-helpPosition)
	var b strings.Builder
	if len(invocation) <= helpPosition-2 {
		fmt.Fprintf(&b, "%-*s%s\n", helpPosition, invocation, lines[0])
		lines = lines[1:]
	} else {
		fmt.Fprintln(&b, invocation)
	}
	for _, l := range lines {
		fmt.Fprintln(&b, indent+l)
	}
	return b.String()
}

// wrapIndented wraps text after prefix, indenting continuation lines.
func wrapIndented(prefix, text string, indent int) string {
	lines := wrapWords(text, helpWidth-indent)
	return prefix + strings.Join(lines, "\n"+strings.Repeat(" ", indent))
}

// wrapWords greedily wraps text into lines of at most width characters.
func wrapWords(text string, width int) []string {
	var lines []string
	line := ""
	for _, w := range strings.Fields(text) {
		switch {
		case line == "":
			line = w
		case len(line)+1+len(w) <= width:
			line += " " + w
		default:
			lines = append(lines, line)
			line = w
		}
	}
	return append(lines, line)
}
