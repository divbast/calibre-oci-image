// SPDX-License-Identifier: GPL-3.0-only
// SPDX-FileCopyrightText: 2026 Aryan Ameri <github@aryan.ameri.coffee>

// Package calibredb is the calibredb that Calibre-Web runs. Calibre-Web
// is given /opt/calibre-bin as its folder of Calibre's programs, and runs
// calibredb there; that is a link to divbast-calibre. The Content Server
// holds Calibre's single-instance lock on the library, so a calibredb of
// Calibre-Web's own could not open it. This one sends the same call to
// the Content Server, as its read-only internal user, and then runs
// Calibre's own calibredb.
package calibredb

import (
	"fmt"
	"io"
	"slices"
	"strings"
)

// Real is Calibre's own calibredb.
const Real = "/opt/calibre/calibredb"

// The three things Calibre-Web gives a call, once it is rewritten.
const (
	// library is the Content Server's library, through the image's own
	// port 8080, so the image sees these calls as it sees every other
	// request, and does not restart the Content Server under one.
	library = "http://127.0.0.1:8080/#books"
	// user is the Content Server's read-only internal user.
	user = "calibre-web"
	// internalFile is Calibre's way to read a password from a file, so it is
	// on no command line.
	internalFile = "<f:/config/calibre/internal-password>"
)

// The library as Calibre-Web names it: its folder, written without a
// trailing slash.
const books = "/books"

// Rewrite returns the arguments Calibre's calibredb is run with, for the
// arguments Calibre-Web gave calibredb. Calibre-Web makes three calls, and
// Rewrite accepts those, in the order and form its code writes them, and
// no other:
//
//	--version
//	export --dont-write-opf --dont-save-cover --with-library /books
//	    --to-dir DIR --formats FORMAT --template NAME ID
//	show_metadata --as-opf ID --with-library /books
//
// In the last two, "--with-library /books" becomes the Content Server's
// library, with the internal user and its password file. A value that
// starts with "-", or is empty, or an ID that is not a number, is
// refused, so no value can pass for an option. The error is one line, and
// names the subcommand.
func Rewrite(args []string) ([]string, error) {
	if len(args) == 0 {
		return nil, refuse("(none)", "calibredb needs a subcommand")
	}
	switch args[0] {
	case "--version":
		if len(args) != 1 {
			return nil, refuse(args[0], "it takes no other argument")
		}
		return slices.Clone(args), nil
	case "export":
		// 0 export; 1, 2 flags; 3, 4 library; 5, 6 --to-dir; 7, 8
		// --formats; 9, 10 --template; 11 the book
		if len(args) == 12 && args[1] == "--dont-write-opf" && args[2] == "--dont-save-cover" &&
			args[3] == "--with-library" && args[4] == books &&
			args[5] == "--to-dir" && args[7] == "--formats" && args[9] == "--template" &&
			value(args[6]) && value(args[8]) && value(args[10]) && number(args[11]) {
			return slices.Concat(args[:4], loginArgs(), args[5:]), nil
		}
	case "show_metadata":
		if len(args) == 5 && args[1] == "--as-opf" && number(args[2]) &&
			args[3] == "--with-library" && args[4] == books {
			return slices.Concat(args[:4], loginArgs()), nil
		}
	default:
		return nil, refuse(args[0], "Calibre-Web runs only export and show_metadata, and --version")
	}
	return nil, refuse(args[0], "its arguments are not the ones Calibre-Web gives")
}

// loginArgs are what follows "--with-library", in place of /books.
func loginArgs() []string {
	return []string{library, "--username", user, "--password", internalFile}
}

// value is true for a value that cannot pass for an option.
func value(s string) bool { return s != "" && !strings.HasPrefix(s, "-") }

// number is true for a book's ID.
func number(s string) bool {
	return s != "" && strings.IndexFunc(s, func(r rune) bool { return r < '0' || r > '9' }) < 0
}

func refuse(sub, why string) error {
	return fmt.Errorf("calibredb %s is refused: %s", sub, why)
}

// Run runs Calibre's calibredb for the arguments Calibre-Web gave calibredb,
// rewritten, with exec, which replaces this process with the program it is
// given, and with environ unchanged. It returns an exit code, and writes
// one line to stderr, only when exec returns: 2 for a call that is
// refused, or 1 when Calibre's calibredb could not be run.
func Run(args, environ []string, exec func(path string, argv, env []string) error, stderr io.Writer) int {
	argv, err := Rewrite(args)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "divbast-calibre:", err)
		return 2
	}
	if err := exec(Real, slices.Concat([]string{Real}, argv), environ); err != nil {
		_, _ = fmt.Fprintln(stderr, "divbast-calibre: running", Real, "failed:", err)
		return 1
	}
	return 0
}
