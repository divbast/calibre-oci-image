// SPDX-License-Identifier: GPL-3.0-only
// SPDX-FileCopyrightText: 2026 Aryan Ameri <github@aryan.ameri.coffee>

package firststart

import (
	"fmt"
	"os"
	"strings"
	"unicode"
	"unicode/utf8"
)

// ReadSecret returns the secret that the variable name holds, or that the
// file the variable name_FILE names holds, the two ways Docker and
// Kubernetes give a container a secret. env looks a variable up, and an
// empty value counts as not set. One newline at the file's end, \n or
// \r\n, is not part of the secret; everything else is. It refuses both
// variables set, neither set, an empty file, a file it cannot read, and a
// secret that is not UTF-8 text. Its errors name the variable or the
// path, never the secret.
func ReadSecret(env func(string) string, name string) (string, error) {
	fileVar := name + "_FILE"
	value, path := env(name), env(fileVar)
	switch {
	case value != "" && path != "":
		return "", fmt.Errorf("both %s and %s are set: set only one of them", name, fileVar)
	case value != "":
		if !utf8.ValidString(value) {
			return "", fmt.Errorf("%s does not hold UTF-8 text", name)
		}
		return value, nil
	case path == "":
		return "", fmt.Errorf("%s is not set: set it to the password, or set %s to the path of a file that holds the password (an empty value counts as not set)", name, fileVar)
	}
	data, err := os.ReadFile(path) //nolint:gosec // G304: the file the operator names to hold the secret
	if err != nil {
		return "", fmt.Errorf("%s names a file that cannot be read: %w", fileVar, err)
	}
	s, ok := strings.CutSuffix(string(data), "\r\n")
	if !ok {
		s = strings.TrimSuffix(s, "\n")
	}
	switch {
	case s == "":
		return "", fmt.Errorf("%s names %s, which is empty", fileVar, path)
	case !utf8.ValidString(s):
		return "", fmt.Errorf("%s names %s, which does not hold UTF-8 text", fileVar, path)
	}
	return s, nil
}

// byteOrderMark is U+FEFF, which some editors write at the start of a
// text file, to mark it as UTF-8.
const byteOrderMark = '\uFEFF'

// CheckHidden refuses what an editor can leave in a password file without
// it showing, and the image applies it to both admin passwords, before any
// other rule: a byte-order mark, anywhere, the start included; and a
// carriage return, \r, which ReadSecret leaves when a file ends in a lone
// \r, or in more than one line end, since it takes off only one \n or
// \r\n. Nothing is taken off quietly, so that a password is never one
// the user did not mean. variable names where the password came from: a
// name that ends in _FILE is a file's, from which ReadSecret takes off
// one line end, and only then does the error say so. The error is one line that
// names variable and what the password holds, and never the password.
func CheckHidden(variable, value string) error {
	switch {
	case strings.HasPrefix(value, string(byteOrderMark)):
		return fmt.Errorf("%s: the password starts with a byte-order mark (U+FEFF), which some editors write at the start of a file: save the password without it", variable)
	case strings.ContainsRune(value, byteOrderMark):
		return fmt.Errorf("%s: the password holds a byte-order mark (U+FEFF), which no password may hold", variable)
	case strings.ContainsRune(value, '\r') && strings.HasSuffix(variable, "_FILE"):
		return fmt.Errorf("%s: the password holds a carriage return (\\r): only the one \\n or \\r\\n that may end the file is taken off, and no password may hold one", variable)
	case strings.ContainsRune(value, '\r'):
		return fmt.Errorf("%s: the password holds a carriage return (\\r), which no password may hold", variable)
	}
	return nil
}

// CheckPassword holds a password to Calibre-Web's default password rules,
// the rules its own valid_password applies with every setting at its
// default, and which the image applies to both admin passwords. variable
// names where the password came from. The error is one line that names
// variable and the first rule the password breaks, in Calibre-Web's
// order, and never the password.
func CheckPassword(variable, value string) error {
	rule := brokenRule(value)
	if rule == "" {
		return nil
	}
	return fmt.Errorf("%s: the password %s (both admin passwords must follow Calibre-Web's default password rules)", variable, rule)
}

// brokenRule returns the first of Calibre-Web's default password rules
// that pw breaks, or "" when it breaks none. Calibre-Web builds them as one
// regular expression, ^(?=.{8,}$)(?=.*?\d)(?=.*?[\p{Ll}])(?=.*?[\p{Lu}])
// (?=.*?[\p{Letter}])(?=.*?[^\p{Letter}\s0-9]), and this mirrors it so:
//
//   - its . matches no newline, so its length rule refuses one anywhere
//     but at the very end; this refuses that last one too, which no login
//     form can send;
//   - the length is counted in Unicode code points, as Python counts it;
//   - \d is any Unicode decimal digit (Nd), and \p{Ll}, \p{Lu} and
//     \p{Letter} are their Unicode categories, by Go's Unicode tables,
//     whose Unicode version may differ from Calibre-Web's Python's;
//   - \s is white space as Go's unicode.IsSpace has it, which may differ
//     from Python's regex module in a few rare characters.
func brokenRule(pw string) string {
	switch {
	case strings.Contains(pw, "\n"):
		return "must not hold a line break"
	case utf8.RuneCountInString(pw) < 8:
		return "needs at least 8 characters"
	case !strings.ContainsFunc(pw, unicode.IsDigit):
		return "needs at least one digit"
	case !strings.ContainsFunc(pw, unicode.IsLower):
		return "needs at least one lower-case letter"
	case !strings.ContainsFunc(pw, unicode.IsUpper):
		return "needs at least one upper-case letter"
	case !strings.ContainsFunc(pw, unicode.IsLetter):
		// Kept as Calibre-Web has it, though the two rules above imply it
		return "needs at least one letter"
	case !strings.ContainsFunc(pw, special):
		return "needs at least one character that is not a letter, a space or a digit 0-9, such as ! or #"
	}
	return ""
}

// special reports whether r counts as a special character to Calibre-Web:
// not a letter, not white space, and not one of the digits 0-9.
func special(r rune) bool {
	return !unicode.IsLetter(r) && !unicode.IsSpace(r) && (r < '0' || r > '9')
}

// CheckCalibrePassword refuses a password the Content Server would refuse
// once first start had written the library: one that holds a control
// character (Cc), an invisible formatting character (Cf), a lone surrogate
// (Cs), or a line or paragraph separator (Zl, Zp), the categories
// Calibre's validate_password refuses, by Go's Unicode tables. The error
// is one line that names variable, and never the password.
func CheckCalibrePassword(variable, value string) error {
	if strings.ContainsFunc(value, func(r rune) bool {
		return unicode.In(r, unicode.Cc, unicode.Cf, unicode.Cs, unicode.Zl, unicode.Zp)
	}) {
		return fmt.Errorf("%s: the password must not hold a control character, a line break or an invisible formatting character, which Calibre's Content Server refuses", variable)
	}
	return nil
}
