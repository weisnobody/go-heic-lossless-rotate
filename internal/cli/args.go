package cli

// An argparse-compatible command-line parser (Go-only; stdlib flag can't
// express upstream's grammar). It reproduces the behavior of the parser
// cli.py's build_parser() creates under Python 3.14 - not just which argv
// are accepted, but argparse's processing ORDER, which decides the exit code
// when an argv has several problems:
//
//   - arguments are processed left to right;
//   - a positional is converted (and checked against its choices) when it is
//     consumed, so `rotate 45 x -h` fails before -h is reached;
//   - -h, -V and -G act as soon as they are reached;
//   - unknown options are collected and reported only at the end, so
//     `--bogus -h` still shows help - including the unknown tail of a
//     combined short-flag group (`-qz` applies -q and defers -z);
//   - the first top-level positional selects the subcommand, and everything
//     after it belongs to the subcommand's parser.
//
// The parse routines below mirror argparse's _parse_optional and
// consume_optional step by step. internal/cli/testdata/argparse_py314.json
// holds the behavior captured from the real parser that the tests check
// against.

import (
	"fmt"
	"io"
	"regexp"
	"strings"
	"unicode"
)

type optKind int

const (
	kindFlag      optKind = iota // store_true
	kindHelp                     // print help, exit 0
	kindVersion                  // print -V text, exit 0
	kindGoVersion                // print -G text, exit 0 (Go-only, docs/COMPAT.md E1)
)

type option struct {
	short, long string // "-q", "--quiet"; either may be empty
	kind        optKind
	dest        string // args field, for kindFlag
	help        string
}

type positional struct {
	name     string
	metavar  string
	optional bool // nargs='?'
	help     string
	// set converts and stores a value; a non-nil error is a usage error.
	set func(a *args, value string) error
}

// parser is one argparse.ArgumentParser: the top level, or a subcommand.
type parser struct {
	prog        string // "heic-rotate", or "heic-rotate rotate"
	name        string // subcommand name; "" at top level
	summary     string // the subcommand's one-line help in the top-level listing
	description string
	opts        []option
	positionals []positional
	subcommands []*parser // top level only
	version     func() string
	goVersion   func() string
}

// args is the parsed namespace (Python's argparse.Namespace).
type args struct {
	cmd               string
	rotation          int
	input             string
	output            string // "" when absent, like Python's None (both mean "default")
	quiet             bool
	dryRun            bool
	force             bool
	noExifSync        bool
	ignoreTamperCheck bool
}

func (a *args) setFlag(dest string) {
	switch dest {
	case "quiet":
		a.quiet = true
	case "dry_run":
		a.dryRun = true
	case "force":
		a.force = true
	case "no_exif_sync":
		a.noExifSync = true
	case "ignore_tamper_check":
		a.ignoreTamperCheck = true
	default:
		panic("unknown option dest " + dest)
	}
}

// exitError is a parse outcome that ends the program: help or version
// output (code 0), or a usage error (code 2).
type exitError struct{ code int }

// usageError prints argparse's "usage: ...\nprog: error: msg" to stderr.
func (p *parser) usageError(stderr io.Writer, format string, args ...any) *exitError {
	fmt.Fprintf(stderr, "%s%s: error: %s\n", p.usage(), p.prog, fmt.Sprintf(format, args...))
	return &exitError{2}
}

// negativeNumber is argparse's _negative_number_matcher: such arguments are
// positionals, since none of this parser's options look like negative
// numbers.
var negativeNumber = regexp.MustCompile(`^-\d+$|^-\d*\.\d+$`)

// optionMatch is argparse's (action, option_string, explicit_arg) tuple.
type optionMatch struct {
	opt         *option
	optString   string
	explicitArg *string
	sep         bool // the explicit argument came after '=' ("-q=1", "--quiet=1")
}

func (p *parser) lookup(optString string) *option {
	for i := range p.opts {
		if o := &p.opts[i]; optString == o.short || optString == o.long {
			return o
		}
	}
	return nil
}

// parseOptional mirrors argparse's _parse_optional: is tok an option, and if
// so which? It returns (nil, false) for a positional, and (match with nil
// opt, true) for an unrecognized option.
func (p *parser) parseOptional(tok string, stderr io.Writer) (*optionMatch, bool, error) {
	if tok == "" || tok[0] != '-' {
		return nil, false, nil
	}
	if o := p.lookup(tok); o != nil {
		return &optionMatch{opt: o, optString: tok}, true, nil
	}
	if len(tok) == 1 {
		return nil, false, nil // "-" is a positional
	}
	if name, value, ok := strings.Cut(tok, "="); ok {
		if o := p.lookup(name); o != nil {
			return &optionMatch{opt: o, optString: name, explicitArg: &value, sep: true}, true, nil
		}
	}
	// Abbreviations (allow_abbrev=True): a unique prefix of a long option,
	// or a known short option followed by more characters.
	matches := p.optionTuples(tok)
	if len(matches) > 1 {
		names := make([]string, len(matches))
		for i, m := range matches {
			names[i] = m.optString
		}
		return nil, false, p.usageError(stderr, "ambiguous option: %s could match %s", tok, strings.Join(names, ", "))
	}
	if len(matches) == 1 {
		return &matches[0], true, nil
	}
	if negativeNumber.MatchString(tok) || strings.Contains(tok, " ") {
		return nil, false, nil
	}
	return &optionMatch{optString: tok}, true, nil // unrecognized: collected for the end
}

// optionTuples mirrors argparse's _get_option_tuples.
func (p *parser) optionTuples(tok string) []optionMatch {
	var out []optionMatch
	if strings.HasPrefix(tok, "--") {
		prefix, explicit, hasEq := strings.Cut(tok, "=")
		for i := range p.opts {
			o := &p.opts[i]
			if o.long != "" && strings.HasPrefix(o.long, prefix) {
				m := optionMatch{opt: o, optString: o.long}
				if hasEq {
					m.explicitArg, m.sep = &explicit, true
				}
				out = append(out, m)
			}
		}
		return out
	}
	// Single dash: "-qf" is "-q" with explicit argument "f".
	short, rest := tok[:2], tok[2:]
	for i := range p.opts {
		o := &p.opts[i]
		switch {
		case o.short == short:
			out = append(out, optionMatch{opt: o, optString: short, explicitArg: &rest})
		case o.long != "" && strings.HasPrefix(o.long, tok):
			out = append(out, optionMatch{opt: o, optString: o.long})
		}
	}
	return out
}

// consumeOptional mirrors argparse's consume_optional for zero-argument
// actions. A combined short-flag group ("-qf") expands one flag at a time:
// an unknown character ends the group, leaving the flags before it in
// effect and the rest ("-z" of "-qz") as an unrecognized argument, returned
// in deferred and reported at the end. An explicit argument that came after
// '=' or starts with '-' ("-q=1", "-f-q", "--quiet=1") is an immediate
// error. The group's flags take effect only after the whole group has been
// read, in order - so "-hz" still shows help.
func (p *parser) consumeOptional(m *optionMatch, a *args, stdout, stderr io.Writer) (deferred string, err error) {
	var actions []*option
	for {
		if m.explicitArg == nil {
			actions = append(actions, m.opt)
			break
		}
		explicit := *m.explicitArg
		if len(m.optString) == 2 && m.optString[1] != '-' && explicit != "" {
			if m.sep || explicit[0] == '-' {
				return "", p.usageError(stderr, "argument %s: ignored explicit argument %s", optionNames(m.opt), pyRepr(explicit))
			}
			actions = append(actions, m.opt)
			next := "-" + explicit[:1]
			o := p.lookup(next)
			if o == nil {
				deferred = "-" + explicit
				break
			}
			m = &optionMatch{opt: o, optString: next}
			if rest := explicit[1:]; rest != "" {
				if rest[0] == '=' {
					rest, m.sep = rest[1:], true
				}
				m.explicitArg = &rest
			}
			continue
		}
		return "", p.usageError(stderr, "argument %s: ignored explicit argument %s", optionNames(m.opt), pyRepr(explicit))
	}
	for _, o := range actions {
		switch o.kind {
		case kindFlag:
			a.setFlag(o.dest)
		case kindHelp:
			fmt.Fprint(stdout, p.formatHelp())
			return "", &exitError{0}
		case kindVersion:
			fmt.Fprintln(stdout, p.version())
			return "", &exitError{0}
		case kindGoVersion:
			fmt.Fprintln(stdout, p.goVersion())
			return "", &exitError{0}
		}
	}
	return deferred, nil
}

func optionNames(o *option) string {
	var names []string
	for _, n := range []string{o.short, o.long} {
		if n != "" {
			names = append(names, n)
		}
	}
	return strings.Join(names, "/")
}

func pyRepr(s string) string { return "'" + strings.ReplaceAll(s, "'", `\'`) + "'" }

// parse parses argv (program name excluded) into a. A non-nil *exitError
// means the program should exit with its code (help/version already
// printed, or a usage error reported).
func (p *parser) parse(argv []string, a *args, stdout, stderr io.Writer) error {
	var unrecognized []string
	dashDash := false
	nextPositional := 0
	for i := 0; i < len(argv); i++ {
		tok := argv[i]
		if !dashDash && tok == "--" {
			dashDash = true // only the first "--" is consumed; later ones are values
			continue
		}
		if !dashDash {
			m, isOpt, err := p.parseOptional(tok, stderr)
			if err != nil {
				return err
			}
			if isOpt {
				if m.opt == nil {
					unrecognized = append(unrecognized, tok)
					continue
				}
				deferred, err := p.consumeOptional(m, a, stdout, stderr)
				if err != nil {
					return err
				}
				if deferred != "" {
					unrecognized = append(unrecognized, deferred)
				}
				continue
			}
		}

		if p.subcommands != nil {
			sub := p.subcommand(tok)
			if sub == nil {
				names := make([]string, len(p.subcommands))
				for j, s := range p.subcommands {
					names[j] = pyRepr(s.name)
				}
				return p.usageError(stderr, "argument {%s}: invalid choice: %s (choose from %s)",
					p.subcommandList(), pyRepr(tok), strings.Join(names, ", "))
			}
			a.cmd = sub.name
			if err := sub.parse(argv[i+1:], a, stdout, stderr); err != nil {
				return err
			}
			if len(unrecognized) > 0 {
				return p.usageError(stderr, "unrecognized arguments: %s", strings.Join(unrecognized, " "))
			}
			return nil
		}

		if nextPositional >= len(p.positionals) {
			unrecognized = append(unrecognized, tok)
			continue
		}
		pos := p.positionals[nextPositional]
		if err := pos.set(a, tok); err != nil {
			return p.usageError(stderr, "argument %s: %v", pos.name, err)
		}
		nextPositional++
	}

	if p.subcommands != nil {
		return p.usageError(stderr, "the following arguments are required: cmd")
	}
	var missing []string
	for _, pos := range p.positionals[nextPositional:] {
		if !pos.optional {
			missing = append(missing, pos.name)
		}
	}
	if len(missing) > 0 {
		return p.usageError(stderr, "the following arguments are required: %s", strings.Join(missing, ", "))
	}
	if len(unrecognized) > 0 {
		return p.usageError(stderr, "unrecognized arguments: %s", strings.Join(unrecognized, " "))
	}
	return nil
}

func (p *parser) subcommand(name string) *parser {
	for _, s := range p.subcommands {
		if s.name == name {
			return s
		}
	}
	return nil
}

func (p *parser) subcommandList() string {
	names := make([]string, len(p.subcommands))
	for i, s := range p.subcommands {
		names[i] = s.name
	}
	return strings.Join(names, ",")
}

// parsePyInt mirrors Python's int(s) for base 10: surrounding Unicode
// White_Space is stripped (not str.isspace()'s extra U+001C-U+001F), an ASCII sign is optional, digits may be any Unicode decimal
// digits ('９０' is 90), and single underscores may separate digits.
// Values outside int range are reported as invalid, which for rotation
// (choices 0/90/180/270) is the same outcome Python reaches.
func parsePyInt(s string) (int, bool) {
	s = strings.TrimFunc(s, unicode.IsSpace) // Unicode White_Space, exactly what int() strips
	neg := false
	if s != "" && (s[0] == '+' || s[0] == '-') {
		neg, s = s[0] == '-', s[1:]
	}
	if s == "" {
		return 0, false
	}
	n, prevUnderscore := 0, true // a leading underscore is invalid
	for _, r := range s {
		if r == '_' {
			if prevUnderscore {
				return 0, false
			}
			prevUnderscore = true
			continue
		}
		d, ok := decimalValue(r)
		if !ok || n > (1<<31)/10 {
			return 0, false
		}
		n, prevUnderscore = n*10+d, false
	}
	if prevUnderscore { // trailing underscore
		return 0, false
	}
	if neg {
		n = -n
	}
	return n, true
}

// decimalValue returns the value of a Unicode decimal digit (category Nd).
// Nd characters come in contiguous runs of ten starting at zero, and the
// unicode.Nd ranges start at a zero, so the value is the offset mod 10.
func decimalValue(r rune) (int, bool) {
	for _, rg := range unicode.Nd.R16 {
		if lo, hi := rune(rg.Lo), rune(rg.Hi); r >= lo && r <= hi {
			return int(r-lo) % 10, true
		}
	}
	for _, rg := range unicode.Nd.R32 {
		if lo, hi := rune(rg.Lo), rune(rg.Hi); r >= lo && r <= hi {
			return int(r-lo) % 10, true
		}
	}
	return 0, false
}
