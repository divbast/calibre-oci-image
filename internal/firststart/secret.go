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
	return fmt.Errorf("%s: the password needs %s (both admin passwords must follow Calibre-Web's default password rules)", variable, rule)
}

// brokenRule returns the first of Calibre-Web's default password rules
// that pw breaks, or "" when it breaks none. Each mirrors a part of the
// regular expression Calibre-Web builds: .{8,}, \d, \p{Ll}, \p{Lu},
// \p{Letter} and [^\p{Letter}\s0-9].
func brokenRule(pw string) string {
	switch {
	case utf8.RuneCountInString(pw) < 8:
		return "at least 8 characters"
	case !strings.ContainsFunc(pw, unicode.IsDigit):
		return "at least one digit"
	case !strings.ContainsFunc(pw, unicode.IsLower):
		return "at least one lower-case letter"
	case !strings.ContainsFunc(pw, unicode.IsUpper):
		return "at least one upper-case letter"
	case !strings.ContainsFunc(pw, unicode.IsLetter):
		// Kept as Calibre-Web has it, though the two rules above imply it
		return "at least one letter"
	case !strings.ContainsFunc(pw, special):
		return "at least one character that is not a letter, a space or a digit 0-9, such as ! or #"
	}
	return ""
}

// special reports whether r counts as a special character to Calibre-Web:
// not a letter, not white space, and not one of the digits 0-9.
func special(r rune) bool {
	return !unicode.IsLetter(r) && !unicode.IsSpace(r) && (r < '0' || r > '9')
}
