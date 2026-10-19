// Package pyfmt reproduces CPython's string formatting for the handful of
// value kinds heic-lossless-rotate interpolates into its messages, so the Go
// port's log lines and error messages are byte-identical to upstream's
// (docs/SPEC.md C5). Each function names the Python expression it mirrors.
package pyfmt

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"unicode"
)

// BytesRepr mirrors repr(b) for a bytes object, e.g. b'irot', b"a'b",
// b'\x00\xff'.
func BytesRepr(b []byte) string {
	quote := byte('\'')
	if slices.Contains(b, '\'') && !slices.Contains(b, '"') {
		quote = '"'
	}
	var sb strings.Builder
	sb.WriteByte('b')
	sb.WriteByte(quote)
	for _, c := range b {
		switch {
		case c == quote || c == '\\':
			sb.WriteByte('\\')
			sb.WriteByte(c)
		case c == '\t':
			sb.WriteString(`\t`)
		case c == '\n':
			sb.WriteString(`\n`)
		case c == '\r':
			sb.WriteString(`\r`)
		case c < ' ' || c >= 0x7f:
			fmt.Fprintf(&sb, `\x%02x`, c)
		default:
			sb.WriteByte(c)
		}
	}
	sb.WriteByte(quote)
	return sb.String()
}

// StrRepr mirrors repr(s) for a str. Printability follows unicode.IsPrint,
// which matches Python's str.isprintable() for every character this tool
// can produce (ASCII and U+FFFD from DecodeASCIIReplace).
func StrRepr(s string) string {
	quote := '\''
	if strings.ContainsRune(s, '\'') && !strings.ContainsRune(s, '"') {
		quote = '"'
	}
	var sb strings.Builder
	sb.WriteRune(quote)
	for _, r := range s {
		switch {
		case r == quote || r == '\\':
			sb.WriteByte('\\')
			sb.WriteRune(r)
		case r == '\t':
			sb.WriteString(`\t`)
		case r == '\n':
			sb.WriteString(`\n`)
		case r == '\r':
			sb.WriteString(`\r`)
		case r == ' ' || (r > ' ' && r < 0x7f):
			sb.WriteRune(r)
		case r >= 0x80 && unicode.IsPrint(r):
			sb.WriteRune(r)
		case r < 0x100:
			fmt.Fprintf(&sb, `\x%02x`, r)
		case r < 0x10000:
			fmt.Fprintf(&sb, `\u%04x`, r)
		default:
			fmt.Fprintf(&sb, `\U%08x`, r)
		}
	}
	sb.WriteRune(quote)
	return sb.String()
}

// DecodeASCIIReplace mirrors b.decode('ascii', 'replace'): every byte >= 0x80
// becomes U+FFFD.
func DecodeASCIIReplace(b []byte) string {
	var sb strings.Builder
	for _, c := range b {
		if c < 0x80 {
			sb.WriteByte(c)
		} else {
			sb.WriteRune('�')
		}
	}
	return sb.String()
}

// Hex08 mirrors f"{v:#010x}" for a 32-bit value: "0x" plus 8 zero-padded
// lowercase hex digits. (Go's own %#010x counts the width differently and
// would print 10 digits.)
func Hex08(v uint32) string {
	return fmt.Sprintf("0x%08x", v)
}

// Bool mirrors str(b) for a bool.
func Bool(b bool) string {
	if b {
		return "True"
	}
	return "False"
}

// EnumerateList mirrors str(list(enumerate(items, 1))) for a list of str,
// e.g. [(1, 'ispe'), (2, 'irot')].
func EnumerateList(items []string) string {
	parts := make([]string, len(items))
	for i, s := range items {
		parts[i] = "(" + strconv.Itoa(i+1) + ", " + StrRepr(s) + ")"
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

// IntBool is one (int, bool) tuple.
type IntBool struct {
	Int  int
	Bool bool
}

// IntBoolList mirrors str(list_of_int_bool_tuples), e.g. [(1, True)].
func IntBoolList(items []IntBool) string {
	parts := make([]string, len(items))
	for i, it := range items {
		parts[i] = "(" + strconv.Itoa(it.Int) + ", " + Bool(it.Bool) + ")"
	}
	return "[" + strings.Join(parts, ", ") + "]"
}
