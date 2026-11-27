// SPDX-License-Identifier: GPL-3.0-only
// SPDX-FileCopyrightText: 2026 Aryan Ameri <github@aryan.ameri.coffee>

package firststart

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestReadSecret is each way Docker and Kubernetes hand a container a
// secret: in the variable, or in a file the _FILE variable names, written
// with or without a newline at its end. Every refusal names the variable
// or the path, and never the secret.
func TestReadSecret(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	file := func(name, content string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	missing := filepath.Join(dir, "missing")
	for _, tt := range []struct {
		name string
		env  map[string]string
		want string
		// each must be in the error; none is wanted when this is empty
		errHas []string
	}{
		{name: "in the variable", env: map[string]string{"PW": "s3cret value"}, want: "s3cret value"},
		{name: "a newline in the variable is kept", env: map[string]string{"PW": "s3cret\n"}, want: "s3cret\n"},
		{name: "in a file", env: map[string]string{"PW_FILE": file("plain", "s3cret")}, want: "s3cret"},
		{name: "a file's newline is stripped", env: map[string]string{"PW_FILE": file("lf", "s3cret\n")}, want: "s3cret"},
		{name: "a file's CRLF is stripped", env: map[string]string{"PW_FILE": file("crlf", "s3cret\r\n")}, want: "s3cret"},
		{name: "only one newline is stripped", env: map[string]string{"PW_FILE": file("lflf", "s3cret\n\n")}, want: "s3cret\n"},
		{name: "a lone CR is kept", env: map[string]string{"PW_FILE": file("cr", "s3cret\r")}, want: "s3cret\r"},
		{name: "spaces are kept", env: map[string]string{"PW_FILE": file("spaces", " s3cret \n")}, want: " s3cret "},
		{
			name:   "both set",
			env:    map[string]string{"PW": "s3cret-var", "PW_FILE": file("both", "s3cret-file")},
			errHas: []string{"PW", "PW_FILE", "both"},
		},
		{name: "neither set", env: map[string]string{}, errHas: []string{"PW", "PW_FILE"}},
		{name: "an empty variable", env: map[string]string{"PW": ""}, errHas: []string{"PW", "PW_FILE"}},
		{name: "an empty file", env: map[string]string{"PW_FILE": file("empty", "")}, errHas: []string{"PW_FILE", filepath.Join(dir, "empty"), "empty"}},
		{name: "a file of one newline", env: map[string]string{"PW_FILE": file("newline", "\n")}, errHas: []string{filepath.Join(dir, "newline"), "empty"}},
		{name: "a file that is not there", env: map[string]string{"PW_FILE": missing}, errHas: []string{"PW_FILE", missing}},
		{name: "a directory", env: map[string]string{"PW_FILE": dir}, errHas: []string{"PW_FILE", dir}},
		{name: "a file that is not UTF-8", env: map[string]string{"PW_FILE": file("latin1", "s3cret\xe9")}, errHas: []string{filepath.Join(dir, "latin1"), "UTF-8"}},
		{name: "a variable that is not UTF-8", env: map[string]string{"PW": "s3cret\xe9"}, errHas: []string{"PW", "UTF-8"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := ReadSecret(func(k string) string { return tt.env[k] }, "PW")
			if len(tt.errHas) == 0 {
				if err != nil || got != tt.want {
					t.Errorf("ReadSecret() = %q, %v; want %q", got, err, tt.want)
				}
				return
			}
			if err == nil {
				t.Fatalf("ReadSecret() = %q, nil; want an error", got)
			}
			if got != "" {
				t.Errorf("ReadSecret() returned %q with its error; want nothing", got)
			}
			for _, s := range tt.errHas {
				if !strings.Contains(err.Error(), s) {
					t.Errorf("ReadSecret() error %q does not name %q", err, s)
				}
			}
			if strings.Contains(err.Error(), "s3cret") {
				t.Errorf("ReadSecret() error %q holds the secret", err)
			}
		})
	}
}

// TestReadSecretUnreadable is a _FILE the running user may not read: it is
// refused, naming the path and never what the file holds.
func TestReadSecretUnreadable(t *testing.T) {
	t.Parallel()
	if os.Geteuid() == 0 {
		t.Skip("root reads past every mode")
	}
	path := filepath.Join(t.TempDir(), "unreadable")
	if err := os.WriteFile(path, []byte("s3cret"), 0o000); err != nil {
		t.Fatal(err)
	}
	got, err := ReadSecret(func(k string) string { return map[string]string{"PW_FILE": path}[k] }, "PW")
	if err == nil || got != "" || !strings.Contains(err.Error(), path) || strings.Contains(err.Error(), "s3cret") {
		t.Errorf("ReadSecret() = %q, %v; want an error naming %s, and not the secret", got, err, path)
	}
}

// TestCheckPassword holds each password to Calibre-Web's default password
// rules: each case breaks one rule alone, but for the letter rule, which
// the lower-case and upper-case rules imply.
func TestCheckPassword(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		value string
		// the rule the error names; none is wanted when this is empty
		rule string
	}{
		{value: "Abcdefg1!"},
		{value: "Pässwörd1!"},
		// U+0661, ARABIC-INDIC DIGIT ONE, is a digit, as \d is to Calibre-Web
		{value: "Abcdefgh١!"},
		// and, being no letter, space or 0-9, it is also a special character
		{value: "Abcdefg1١"},
		{value: "Äb1!äöü", rule: "8 characters"}, // 7 characters in 11 bytes
		{value: "Ab1!xyz", rule: "8 characters"},
		{value: "Abcdefgh!", rule: "digit"},
		{value: "ABCDEFG1!", rule: "lower-case"},
		{value: "abcdefg1!", rule: "upper-case"},
		{value: "Abcdefg12", rule: "not a letter"},
		{value: "Abcdefg1 ", rule: "not a letter"}, // a space is not special
		{value: "12345678!", rule: "lower-case"},   // no letter at all: the first rule broken is named
		// Calibre-Web's .{8,}$ takes no newline but a last one; first start
		// takes none, since a login form cannot send one
		{value: "Abcdefg1!\nx", rule: "line break"},
		{value: "Abcdefg1!\n", rule: "line break"},
	} {
		t.Run(tt.value, func(t *testing.T) {
			t.Parallel()
			err := CheckPassword("PW_VAR", tt.value)
			if tt.rule == "" {
				if err != nil {
					t.Errorf("CheckPassword(%q) = %v, want nil", tt.value, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("CheckPassword(%q) = nil, want an error naming the %s rule", tt.value, tt.rule)
			}
			msg := err.Error()
			if !strings.Contains(msg, "PW_VAR") || !strings.Contains(msg, tt.rule) || !strings.Contains(msg, "Calibre-Web") {
				t.Errorf("CheckPassword(%q) = %q, want it to name PW_VAR, the %s rule and Calibre-Web's rules", tt.value, msg, tt.rule)
			}
			if strings.Contains(msg, tt.value) || strings.Count(msg, "\n") != 0 {
				t.Errorf("CheckPassword(%q) = %q, want one line without the password", tt.value, msg)
			}
		})
	}
}

// TestCheckHidden refuses what an editor can leave in a password file
// without it showing: a byte-order mark anywhere, its start included, and
// a carriage return that ReadSecret left, since it takes off only one \n
// or \r\n at the end. The error is one line that names the variable and
// what the password holds, and never the password.
func TestCheckHidden(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name, value string
		reason      string // what the error says; none is wanted when this is empty
	}{
		{name: "neither", value: "Abcdefg1!"},
		{name: "a byte-order mark at the start", value: "\uFEFFAbcdefg1!", reason: "starts with a byte-order mark"},
		{name: "a byte-order mark inside", value: "Abcd\uFEFFefg1!", reason: "holds a byte-order mark"},
		{name: "a byte-order mark at the end", value: "Abcdefg1!\uFEFF", reason: "holds a byte-order mark"},
		{name: "a byte-order mark twice", value: "\uFEFF\uFEFFAbcdefg1!", reason: "starts with a byte-order mark"},
		{name: "a carriage return at the end", value: "Abcdefg1!\r", reason: "holds a carriage return"},
		{name: "a carriage return inside", value: "Abcd\refg1!", reason: "holds a carriage return"},
		{name: "a line end left from two", value: "Abcdefg1!\r\n", reason: "holds a carriage return"},
		{name: "a byte-order mark and a carriage return", value: "\uFEFFAbcdefg1!\r", reason: "starts with a byte-order mark"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := CheckHidden("PW_VAR", tt.value)
			if tt.reason == "" {
				if err != nil {
					t.Errorf("CheckHidden() = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("CheckHidden() = nil, want an error that says the password %s", tt.reason)
			}
			msg := err.Error()
			if !strings.HasPrefix(msg, "PW_VAR: the password "+tt.reason) {
				t.Errorf("CheckHidden() = %q, want it to begin %q", msg, "PW_VAR: the password "+tt.reason)
			}
			if strings.Contains(msg, "Abcd") || strings.Contains(msg, "\n") || strings.ContainsRune(msg, '\r') || strings.ContainsRune(msg, '\uFEFF') {
				t.Errorf("CheckHidden() = %q, want one line without the password, or what it holds", msg)
			}
		})
	}
}

// TestCheckHiddenLineEnd is a carriage return in a password from each
// source. For one from a _FILE, the error says that only the one \n or
// \r\n that may end the file is taken off. For one from the variable
// itself, where nothing is taken off, it says nothing of that.
func TestCheckHiddenLineEnd(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		variable string
		file     bool
	}{
		{"PW_VAR", false},
		{"PW_VAR_FILE", true},
	} {
		t.Run(tt.variable, func(t *testing.T) {
			t.Parallel()
			err := CheckHidden(tt.variable, "Abcd\refg1!")
			if err == nil {
				t.Fatal("CheckHidden() = nil, want an error")
			}
			msg := err.Error()
			if want := tt.variable + ": the password holds a carriage return"; !strings.HasPrefix(msg, want) {
				t.Errorf("CheckHidden() = %q, want it to begin %q", msg, want)
			}
			if got := strings.Contains(msg, "taken off"); got != tt.file {
				t.Errorf("CheckHidden() = %q; it says what is taken off: %t, want %t", msg, got, tt.file)
			}
		})
	}
}

// TestCheckCalibrePassword refuses a character of each kind Calibre's
// Content Server refuses in a password: control characters, invisible
// formatting characters, and line and paragraph separators. Calibre's
// fifth kind, lone surrogates, cannot be in UTF-8 text, which ReadSecret
// refuses first.
func TestCheckCalibrePassword(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name, value string
		refused     bool
	}{
		{name: "letters, digits and signs", value: "Pässwörd1!"},
		{name: "a space", value: "Abc defg1!"},
		{name: "a tab (Cc)", value: "Abcdefg1!\t", refused: true},
		{name: "a NUL (Cc)", value: "Abcd\x00efg1!", refused: true},
		{name: "DEL (Cc)", value: "Abcdefg1!\x7f", refused: true},
		{name: "a zero-width space (Cf)", value: "Abcd\u200befg1!", refused: true},
		{name: "a soft hyphen (Cf)", value: "Abcd\u00adefg1!", refused: true},
		{name: "a line separator (Zl)", value: "Abcd\u2028efg1!", refused: true},
		{name: "a paragraph separator (Zp)", value: "Abcd\u2029efg1!", refused: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := CheckCalibrePassword("PW_VAR", tt.value)
			if !tt.refused {
				if err != nil {
					t.Errorf("CheckCalibrePassword() = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), "PW_VAR") || !strings.Contains(err.Error(), "Calibre") {
				t.Errorf("CheckCalibrePassword() = %v, want an error naming PW_VAR and Calibre's rule", err)
			}
			if err != nil && strings.Contains(err.Error(), tt.value) {
				t.Errorf("CheckCalibrePassword() = %q, which holds the password", err)
			}
		})
	}
}
