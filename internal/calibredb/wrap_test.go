// SPDX-License-Identifier: GPL-3.0-only
// SPDX-FileCopyrightText: 2026 Aryan Ameri <github@aryan.ameri.coffee>

package calibredb_test

import (
	"bytes"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/divbast/calibre-oci-image/internal/calibredb"
)

// The call Calibre-Web makes for each of its three uses, as its code
// builds them, with a temporary folder, a format, a template and a book
// number as it gives them.
var (
	export = []string{
		"export", "--dont-write-opf", "--dont-save-cover", "--with-library", "/books",
		"--to-dir", "/tmp/calibre_web/instance_f4234b1a2a5c", "--formats", "epub",
		"--template", "cd584da8-28d0-4804-bcf0-babbf2e50993", "12",
	}
	showMetadata = []string{"show_metadata", "--as-opf", "12", "--with-library", "/books"}
)

// login is what replaces "--with-library /books".
var login = []string{
	"--with-library", "http://127.0.0.1:8080/#books",
	"--username", "calibre-web", "--password", "<f:/config/calibre/internal-password>",
}

// with returns args with the value after name replaced by the elements of
// repl, and name itself kept.
func with(args []string, name string, repl ...string) []string {
	i := slices.Index(args, name)
	return slices.Concat(args[:i+1], repl, args[i+2:])
}

func TestRewrite(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		args    []string
		want    []string
		refusal string // the error must hold this, and be one line
	}{
		{name: "version", args: []string{"--version"}, want: []string{"--version"}},
		{
			name: "export", args: export,
			want: slices.Concat(export[:3], login, export[5:]),
		},
		{
			name: "show_metadata", args: showMetadata,
			want: slices.Concat(showMetadata[:3], login),
		},
		{name: "none", args: nil, refusal: "(none)"},
		{name: "add", args: []string{"add", "--with-library", "/books", "/tmp/a.epub"}, refusal: "add"},
		{name: "remove", args: []string{"remove", "--with-library", "/books", "1"}, refusal: "remove"},
		{name: "set_metadata", args: []string{"set_metadata", "--with-library", "/books", "1"}, refusal: "set_metadata"},
		{name: "list", args: []string{"list"}, refusal: "list"},
		{name: "an option first", args: []string{"--with-library", "/books", "list"}, refusal: "--with-library"},
		{name: "version with more", args: []string{"--version", "add"}, refusal: "--version"},
		{name: "export, another library", args: with(export, "--with-library", "/other"), refusal: "export"},
		{name: "export, a trailing slash", args: with(export, "--with-library", "/books/"), refusal: "export"},
		{name: "export, a URL", args: with(export, "--with-library", "http://127.0.0.1:18080/#books"), refusal: "export"},
		{name: "show_metadata, another library", args: with(showMetadata, "--with-library", "/other"), refusal: "show_metadata"},
		{name: "export, a second library", args: append(slices.Clone(export), "--with-library", "/books"), refusal: "export"},
		{name: "show_metadata, a second library", args: append(slices.Clone(showMetadata), "--with-library", "/books"), refusal: "show_metadata"},
		{name: "export, a library as a value", args: with(export, "--formats", "--with-library"), refusal: "export"},
		{name: "export, the user named", args: append(slices.Clone(export), "--username", "admin"), refusal: "export"},
		{name: "export, a missing option", args: slices.Delete(slices.Clone(export), 2, 3), refusal: "export"},
		{name: "export, options in another order", args: slices.Concat(export[:1], export[3:5], export[1:3], export[5:]), refusal: "export"},
		{name: "export, no book", args: export[:len(export)-1], refusal: "export"},
		{name: "export, book not a number", args: append(slices.Clone(export[:len(export)-1]), "1 2"), refusal: "export"},
		{name: "export, book is an option", args: append(slices.Clone(export[:len(export)-1]), "--all"), refusal: "export"},
		{name: "export, empty format", args: with(export, "--formats", ""), refusal: "export"},
		{name: "show_metadata, without --as-opf", args: []string{"show_metadata", "12", "--with-library", "/books"}, refusal: "show_metadata"},
		{name: "show_metadata, book not a number", args: []string{"show_metadata", "--as-opf", "x", "--with-library", "/books"}, refusal: "show_metadata"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			orig := slices.Clone(tt.args)
			got, err := calibredb.Rewrite(tt.args)
			if !slices.Equal(tt.args, orig) {
				t.Errorf("Rewrite changed its argument: %q, was %q", tt.args, orig)
			}
			if tt.refusal == "" {
				if err != nil || !slices.Equal(got, tt.want) {
					t.Errorf("Rewrite(%q) = %q, %v; want %q", tt.args, got, err, tt.want)
				}
				return
			}
			switch {
			case err == nil:
				t.Errorf("Rewrite(%q) = %q, want a refusal naming %q", tt.args, got, tt.refusal)
			case !strings.Contains(err.Error(), tt.refusal) || strings.Contains(err.Error(), "\n") || got != nil:
				t.Errorf("Rewrite(%q) = %q, %q; want one line naming %q, and no arguments", tt.args, got, err, tt.refusal)
			}
		})
	}
}

func TestRun(t *testing.T) {
	t.Parallel()
	env := []string{"A=1", "B=2"}

	type call struct {
		path      string
		argv, env []string
	}
	t.Run("runs Calibre's calibredb with the environment unchanged", func(t *testing.T) {
		t.Parallel()
		var got call
		var stderr bytes.Buffer
		code := calibredb.Run(showMetadata, env, func(path string, argv, env []string) error {
			got = call{path, argv, env}
			return nil
		}, &stderr)
		want := call{calibredb.Real, slices.Concat([]string{calibredb.Real}, showMetadata[:3], login), env}
		if code != 0 || got.path != want.path || !slices.Equal(got.argv, want.argv) || !slices.Equal(got.env, want.env) || stderr.Len() != 0 {
			t.Errorf("Run = %d, %+v, stderr %q; want 0, %+v", code, got, stderr.String(), want)
		}
	})
	t.Run("a refusal runs nothing, and says one line", func(t *testing.T) {
		t.Parallel()
		var stderr bytes.Buffer
		code := calibredb.Run([]string{"add", "x"}, env, func(string, []string, []string) error {
			t.Error("Run started a program for a refused call")
			return nil
		}, &stderr)
		if code != 2 || strings.Count(stderr.String(), "\n") != 1 || !strings.Contains(stderr.String(), "add") {
			t.Errorf("Run = %d, %q; want 2 and one line naming add", code, stderr.String())
		}
	})
	t.Run("a failed start is one line", func(t *testing.T) {
		t.Parallel()
		var stderr bytes.Buffer
		code := calibredb.Run([]string{"--version"}, env, func(string, []string, []string) error {
			return errors.New("no such file")
		}, &stderr)
		if code != 1 || strings.Count(stderr.String(), "\n") != 1 || !strings.Contains(stderr.String(), "no such file") {
			t.Errorf("Run = %d, %q; want 1 and one line", code, stderr.String())
		}
	})
}
