// SPDX-License-Identifier: GPL-3.0-only
// SPDX-FileCopyrightText: 2026 Aryan Ameri <github@aryan.ameri.coffee>

package firststart

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
)

const (
	// Python is Calibre-Web's own Python, in its venv.
	Python = "/app/venv/bin/python"
	// WebHelper writes Calibre-Web's first settings, from JSON on stdin,
	// and reads its login header.
	WebHelper = "/usr/local/share/divbast-calibre/calibre_web_init.py"
	// AppDB is Calibre-Web's settings database, in its config directory.
	AppDB = "app.db"
)

// CalibreWebSettings are the settings first start gives Calibre-Web, each
// by the name of its column in app.db. Every other setting keeps
// Calibre-Web's default.
type CalibreWebSettings struct {
	// CalibreDir is the Calibre library.
	CalibreDir string `json:"config_calibre_dir"`
	// BinariesDir holds Calibre's ebook-convert and calibredb. The helper
	// also writes the converter's path, that folder's ebook-convert, as
	// Calibre-Web's own settings form does.
	BinariesDir string `json:"config_binariesdir"`
	// KepubifyPath is kepubify.
	KepubifyPath string `json:"config_kepubifypath"`
	// RarfileLocation is unrar.
	RarfileLocation string `json:"config_rarfile_location"`
	// Port is the port Calibre-Web listens on.
	Port int `json:"config_port"`
	// Logfile is where Calibre-Web logs.
	Logfile string `json:"config_logfile"`
	// Uploading allows uploads.
	Uploading bool `json:"config_uploading"`
}

// webRequest is the JSON calibre_web_init.py init reads from stdin.
type webRequest struct {
	Appdb    string              `json:"appdb"`
	Password string              `json:"password"`
	Settings *CalibreWebSettings `json:"settings"`
}

// EnsureCalibreWeb creates Calibre-Web's settings database in dir, app.db,
// when dir holds none, and reports whether it created one. It holds
// admin, with adminPW, and the settings *s. calibre_web_init.py makes it,
// under Calibre-Web's own code, reading the password from stdin. When
// anything fails, it leaves no app.db, so the next start tries again.
func EnsureCalibreWeb(ctx context.Context, run Runner, dir, adminPW string, s *CalibreWebSettings) (bool, error) {
	path := filepath.Join(dir, AppDB)
	if present, err := exists(path); err != nil || present {
		return false, err
	}
	if adminPW == "" {
		return false, fmt.Errorf("no password for Calibre-Web's %s, which %s would hold", AdminUser, path)
	}
	tmp := filepath.Join(dir, "."+AppDB+".new")
	stdin, err := json.Marshal(webRequest{Appdb: tmp, Password: adminPW, Settings: s}) //nolint:gosec // G117: the password goes to the helper on its stdin, which is the point
	placed := false
	if err == nil {
		placed, err = createDatabase(ctx, run, []string{Python, WebHelper, "init"}, stdin, tmp, path)
	}
	switch {
	case err != nil && placed:
		return true, fmt.Errorf("wrote Calibre-Web's first settings in %s, but could not make them durable: %w", dir, err)
	case err != nil:
		return false, fmt.Errorf("could not write Calibre-Web's first settings in %s: %w", dir, err)
	}
	return true, nil
}
