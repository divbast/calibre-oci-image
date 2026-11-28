// SPDX-License-Identifier: GPL-3.0-only
// SPDX-FileCopyrightText: 2026 Aryan Ameri <github@aryan.ameri.coffee>

package pins

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"
)

// BoundsPath is the Renovate preset Generate writes, which renovate.json
// extends.
const BoundsPath = "python/renovate-bounds.json"

// Bound is how far Renovate may move one pin of pins.txt: Allowed, every
// specifier the image's packages and upstream ask of it, as one PEP 440
// specifier set, and Sources, who asks for what.
type Bound struct {
	Name, Allowed string
	Sources       []string
}

// requiresDist is a Requires-Dist entry: a name, extras in brackets, then
// a specifier, bare or in parentheses, then a marker after ';'.
var requiresDist = regexp.MustCompile(`^([A-Za-z0-9][A-Za-z0-9._-]*)\s*(?:\[[^\]]*\])?\s*(?:\(([^)]*)\)|([^;@()]*))\s*(?:;\s*(.*))?$`)

// ParseRequiresDist reads one Requires-Dist entry, as PyPI gives it, into
// the package it names, its specifier with no spaces, or "" for any
// version, and its marker, or "". A URL in place of a version is an error:
// PyPI takes no release that names one.
func ParseRequiresDist(entry string) (name, spec, marker string, err error) {
	before, _, _ := strings.Cut(entry, ";")
	if strings.Contains(before, "@") {
		return "", "", "", fmt.Errorf("the requirement %q names a URL (@), which this tool does not read", entry)
	}
	m := requiresDist.FindStringSubmatch(strings.TrimSpace(entry))
	if m == nil {
		return "", "", "", fmt.Errorf("%q is no requirement this tool reads", entry)
	}
	return m[1], strings.ReplaceAll(m[2]+m[3], " ", ""), strings.TrimSpace(m[4]), nil
}

// Bounds gives each pin of pins, but a KindLeftOut one, every specifier
// asked of it: upstream's, from reqs, for a runtime pin; and each other
// package's whose Requires-Dist names it, from locked for a runtime pin
// and from backends for a build one, since each lock is one environment.
// A Requires-Dist marker is judged for each of envs, as pip check judges
// it, and a requirement any of them takes counts. A pin gets a Bound only
// when some specifier could refuse a newer version: a lower bound alone
// never does. A marker this tool cannot judge is an error naming the
// package and the requirement. Bounds come in the order of their
// normalised names.
func Bounds(pins []Pin, reqs []Requirement, locked, backends []Locked, envs []MarkerEnv) ([]Bound, error) {
	upstream := map[string]string{}
	for _, r := range reqs {
		upstream[Normalize(r.Name)] = r.Specifier
	}
	runtime, err := asked(locked, envs)
	if err != nil {
		return nil, err
	}
	built, err := asked(backends, envs)
	if err != nil {
		return nil, err
	}
	var out []Bound
	for _, p := range pins {
		if p.Kind == KindLeftOut {
			continue
		}
		key := Normalize(p.Name)
		var sources, specs []string
		by := runtime
		if p.Kind == KindBuild {
			by = built
		} else if s := upstream[key]; s != "" {
			sources, specs = append(sources, "upstream asks for "+s), append(specs, s)
		}
		for _, a := range by[key] {
			sources, specs = append(sources, a.who+" requires "+a.spec), append(specs, a.spec)
		}
		if b, ok := bound(p.Name, sources, specs); ok {
			out = append(out, b)
		}
	}
	slices.SortFunc(out, func(a, b Bound) int { return cmp.Compare(Normalize(a.Name), Normalize(b.Name)) })
	return out, nil
}

// ask is one package's specifier for another: who is "name version".
type ask struct{ who, spec string }

// asked reads the Requires-Dist of each package of pkgs, and returns, by
// the normalised name of the package each names, what each asks of it,
// in the order of the asking packages' normalised names. A package's
// requirement of itself is not read.
func asked(pkgs []Locked, envs []MarkerEnv) (map[string][]ask, error) {
	pkgs = slices.Clone(pkgs)
	slices.SortFunc(pkgs, func(a, b Locked) int { return cmp.Compare(Normalize(a.Name), Normalize(b.Name)) })
	by := map[string][]ask{}
	var errs []error
	for _, l := range pkgs {
		who := l.Name + " " + l.Version
		for _, entry := range l.Requires {
			name, spec, marker, perr := ParseRequiresDist(entry)
			if perr != nil {
				errs = append(errs, fmt.Errorf("%s: %w", who, perr))
				continue
			}
			key := Normalize(name)
			if key == Normalize(l.Name) || spec == "" {
				continue
			}
			takes, merr := anyEnv(marker, envs)
			if merr != nil {
				errs = append(errs, fmt.Errorf("%s requires %q: %w", who, entry, merr))
				continue
			}
			if takes {
				by[key] = append(by[key], ask{who: who, spec: spec})
			}
		}
	}
	return by, errors.Join(errs...)
}

// anyEnv reports whether marker, "" for none, holds for any of envs.
func anyEnv(marker string, envs []MarkerEnv) (bool, error) {
	if marker == "" {
		return true, nil
	}
	for i := range envs {
		ok, err := EvalMarker(marker, envs[i])
		if err != nil || ok {
			return ok, err
		}
	}
	return false, nil
}

// bound is name's Bound from its sources and their specifiers: each
// clause once, in order, or none when no clause could refuse a newer
// version.
func bound(name string, sources, specs []string) (Bound, bool) {
	var clauses []string
	for _, s := range specs {
		for c := range strings.SplitSeq(s, ",") {
			if c = strings.TrimSpace(c); c != "" && !slices.Contains(clauses, c) {
				clauses = append(clauses, c)
			}
		}
	}
	if !slices.ContainsFunc(clauses, func(c string) bool { return !strings.HasPrefix(c, ">") }) {
		return Bound{}, false
	}
	slices.Sort(clauses)
	return Bound{Name: name, Allowed: strings.Join(clauses, ","), Sources: sources}, true
}

// boundsDescription opens the file WriteBounds writes.
const boundsDescription = "Generated by go run ./cmd/divbast-pins: each pin of python/pins.txt that a package the image installs, " +
	"or upstream, bounds from above, with every specifier asked of it, so Renovate proposes no version the build refuses. " +
	"Do not edit it: change the pins, then run go run ./cmd/divbast-pins again."

// WriteBounds writes bounds as a Renovate preset: one package rule each,
// matching the pin by its name as pins.txt writes it, in the order given,
// as two-space JSON with a newline at its end. The same bounds always give
// the same bytes.
func WriteBounds(w io.Writer, bounds []Bound) error {
	type rule struct {
		Description       string   `json:"description"`
		MatchManagers     []string `json:"matchManagers"`
		MatchDatasources  []string `json:"matchDatasources"`
		MatchPackageNames []string `json:"matchPackageNames"`
		AllowedVersions   string   `json:"allowedVersions"`
	}
	preset := struct {
		Schema       string `json:"$schema"`
		Description  string `json:"description"`
		PackageRules []rule `json:"packageRules"`
	}{Schema: "https://docs.renovatebot.com/renovate-schema.json", Description: boundsDescription, PackageRules: []rule{}}
	for _, b := range bounds {
		preset.PackageRules = append(preset.PackageRules, rule{
			Description:   b.Name + ": " + strings.Join(b.Sources, "; ") + ".",
			MatchManagers: []string{"custom.regex"}, MatchDatasources: []string{"pypi"},
			MatchPackageNames: []string{b.Name}, AllowedVersions: b.Allowed,
		})
	}
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	return enc.Encode(preset)
}
