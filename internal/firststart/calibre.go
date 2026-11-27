// SPDX-License-Identifier: GPL-3.0-only
// SPDX-FileCopyrightText: 2026 Aryan Ameri <github@aryan.ameri.coffee>

package firststart

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

const (
	// CalibreDebug is Calibre's calibre-debug, which runs a Python file in
	// Calibre's own Python, with Calibre's modules.
	CalibreDebug = "/opt/calibre/calibre-debug"
	// UsersHelper creates the Content Server's users, from JSON on stdin.
	UsersHelper = "/usr/local/share/divbast-calibre/calibre_users.py"
	// UsersFile is the Content Server's user database, in its config
	// directory.
	UsersFile = "users.sqlite"
	// InternalPasswordFile holds the internal user's password, in the
	// Content Server's config directory.
	InternalPasswordFile = "internal-password"
	// AdminUser is the first account of both apps.
	AdminUser = "admin"
	// InternalUser is the read-only Content Server user that Calibre-Web's
	// calibredb runs as.
	InternalUser = "calibre-web"
)

// usersRequest is the JSON calibre_users.py reads from stdin.
type usersRequest struct {
	Userdb string `json:"userdb"`
	Users  []user `json:"users"`
}

// user is one user calibre_users.py adds.
type user struct {
	Name     string `json:"name"`
	Password string `json:"password"`
	Readonly bool   `json:"readonly"`
	// ChangePasswordViaHTTP lets the user change its own password through
	// the Content Server's /users/change-pw
	ChangePasswordViaHTTP bool `json:"allow_change_password_via_http"`
}

// EnsureCalibreUsers creates the Content Server's user database in dir,
// users.sqlite, when dir holds none, and reports whether it created one.
// It holds admin, with adminPW and write access, and the read-only
// internal user, with a password of 32 bytes from random, written as
// base64url to internal-password, mode 0600, before the database is
// made. admin may change its own password over HTTP, as Calibre's default
// is; the internal user, the image's own account, may not.
// calibre_users.py makes the database, under Calibre's own code, reading
// every password from stdin. When anything fails before users.sqlite is
// in place, it leaves neither file, so the next start tries again. Once
// users.sqlite is in place, the password file stays with it, even when
// the directory then fails to sync.
func EnsureCalibreUsers(ctx context.Context, run Runner, dir, adminPW string, random io.Reader) (bool, error) {
	path := filepath.Join(dir, UsersFile)
	if present, err := exists(path); err != nil || present {
		return false, err
	}
	if adminPW == "" {
		return false, fmt.Errorf("no password for the Content Server's %s, which %s would hold", AdminUser, path)
	}
	key := make([]byte, 32)
	if _, err := io.ReadFull(random, key); err != nil {
		return false, fmt.Errorf("could not make a password for the internal user %s: %w", InternalUser, err)
	}
	internal := base64.RawURLEncoding.EncodeToString(key)
	pwFile := filepath.Join(dir, InternalPasswordFile)
	// One left by a start that stopped before users.sqlite was made is no
	// user's password
	if err := os.Remove(pwFile); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return false, err
	}
	if err := writeNew(pwFile, internal, os.Link); err != nil {
		return false, fmt.Errorf("could not write the internal user's password to %s: %w", pwFile, err)
	}
	tmp := filepath.Join(dir, "."+UsersFile+".new")
	stdin, err := json.Marshal(usersRequest{
		Userdb: tmp,
		Users: []user{
			{Name: AdminUser, Password: adminPW, Readonly: false, ChangePasswordViaHTTP: true},
			{Name: InternalUser, Password: internal, Readonly: true, ChangePasswordViaHTTP: false},
		},
	})
	placed := false
	if err == nil {
		placed, err = createDatabase(ctx, run, []string{CalibreDebug, "-e", UsersHelper}, stdin, tmp, path)
	}
	switch {
	case err != nil && placed:
		return true, fmt.Errorf("created the Content Server's users in %s, but could not make them durable: %w", dir, err)
	case err != nil:
		return false, errors.Join(fmt.Errorf("could not create the Content Server's users in %s: %w", dir, err), os.Remove(pwFile))
	}
	return true, nil
}
