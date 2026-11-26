// SPDX-License-Identifier: GPL-3.0-only
// SPDX-FileCopyrightText: 2026 Aryan Ameri <github@aryan.ameri.coffee>

package reload

import (
	"net/http"
	"slices"
	"strings"
)

// App is one of the two apps that share the library.
type App int

const (
	// ContentServer is Calibre's Content Server, calibre-server, behind
	// port 8080.
	ContentServer App = iota
	// CalibreWeb is Calibre-Web, behind port 8083.
	CalibreWeb
)

// String is the app's name, as the supervisor names its child.
func (a App) String() string {
	if a == ContentServer {
		return "calibre-server"
	}
	return "calibre-web"
}

// other is the app that is not a.
func (a App) other() App {
	if a == ContentServer {
		return CalibreWeb
	}
	return ContentServer
}

// Mutating reports whether a request to app can change the library: its
// metadata.db. path is the path as the app routes it.
//
// For the Content Server, the answer comes from its own routes: each says
// whether it writes to the library, and for /cdb/cmd/{which} each command
// does. A path that no route takes, or a method that its route does not
// take, counts as one that writes.
//
// For Calibre-Web, GET and HEAD write nothing, but for the few routes that
// do write, which are listed. Any other method writes, but for the routes
// listed as changing nothing in the library.
func Mutating(app App, method, path string) bool {
	if app == ContentServer {
		return contentServerMutating(method, path)
	}
	return calibreWebMutating(method, path)
}

// The method sets of the Content Server's routes.
var (
	getHead     = []string{http.MethodGet, http.MethodHead}
	postOnly    = []string{http.MethodPost}
	postable    = []string{http.MethodGet, http.MethodPost, http.MethodHead}
	getHeadPut  = []string{http.MethodGet, http.MethodHead, http.MethodPut}
	cdbCommands = "/cdb/cmd/{which}/{version=0}"
)

// csRoute is one of the Content Server's routes, written as calibre writes
// it, with the methods it takes and whether it writes to the library.
type csRoute struct {
	route   string
	methods []string
	writes  bool
}

// contentServerRoutes is every route of calibre-server 9.19.0, from the
// @endpoint decorators in src/calibre/srv/*.py, with the methods each takes
// and its needs_db_write flag. The routes that write are the ones that
// change metadata.db; the others write nothing there, though some write the
// users' own settings, which live elsewhere.
var contentServerRoutes = []csRoute{
	{"/ajax/book/{book_id}/{library_id=None}", getHead, false},
	{"/ajax/books/{library_id=None}", getHead, false},
	{"/ajax/categories/{library_id=None}", getHead, false},
	{"/ajax/category/{encoded_name}/{library_id=None}", getHead, false},
	{"/ajax/books_in/{encoded_category}/{encoded_item}/{library_id=None}", getHead, false},
	{"/ajax/search/{library_id=None}", getHead, false},
	{"/ajax/library-info", getHead, false},
	{"/book-manifest/{book_id}/{fmt}", getHead, false},
	{"/book-file/{book_id}/{fmt}/{size}/{mtime}/{+name}", getHead, false},
	{"/book-get-last-read-position/{library_id}/{+which}", getHead, false},
	{"/book-set-last-read-position/{library_id}/{book_id}/{+fmt}", postOnly, true},
	{"/book-get-annotations/{library_id}/{+which}", getHead, false},
	{"/book-update-annotations/{library_id}/{book_id}/{+fmt}", postOnly, true},
	{"/book-get-storage/{library_id}/{book_id}/{+fmt}", getHead, false},
	{"/book-set-storage/{library_id}/{book_id}/{+fmt}", postOnly, true},
	{`/mathjax/{+which=""}`, getHead, false},
	{cdbCommands, postOnly, true},
	{"/cdb/add-book/{job_id}/{add_duplicates}/{filename}/{library_id=None}", postOnly, true},
	{"/cdb/delete-books/{book_ids}/{library_id=None}", postOnly, true},
	{"/cdb/set-cover/{book_id}/{library_id=None}", postOnly, true},
	{"/cdb/set-fields/{book_id}/{library_id=None}", postOnly, true},
	{"/cdb/copy-to-library/{target_library_id}/{library_id=None}", postOnly, true},
	{"/", getHead, false},
	{"/index.js.map", getHead, false},
	{"/robots.txt", getHead, false},
	{"/service_worker.js", getHead, false},
	{"/manifest.json", getHead, false},
	{"/ajax-setup", getHead, false},
	{"/console-print", postOnly, false},
	{"/interface-data/update/{translations_hash=None}", getHead, false},
	{"/interface-data/books-init", getHead, false},
	{"/interface-data/init", getHead, false},
	{"/interface-data/newly-added", getHead, false},
	{"/interface-data/more-books", postable, false},
	{"/interface-data/set-session-data", postable, false},
	{"/interface-data/get-books", getHead, false},
	{"/interface-data/book-metadata/{book_id=0}", getHead, false},
	{"/web-search/{book_id}/{field}/{item_val}", getHead, false},
	{"/interface-data/tag-browser", getHead, false},
	{"/interface-data/browse-field/{field}", getHead, false},
	{"/interface-data/field-names/{field}", getHead, false},
	{"/interface-data/field-id-map/{field}", getHead, false},
	{"/comic-manifest/{book_id}", getHead, false},
	{"/comic-page/{book_id}/{page}", getHead, false},
	{"/static/{+what}", getHead, false},
	{"/favicon.png", getHead, false},
	{"/favicon.svg", getHead, false},
	{"/favicon-192.png", getHead, false},
	{"/apple-touch-icon.png", getHead, false},
	{"/icon/{+which}", getHead, false},
	{"/reader-background/{encoded_fname}", getHead, false},
	{"/reader-profiles/get-all", getHead, false},
	{"/reader-profiles/save", postOnly, false},
	{"/get/{what}/{book_id}/{library_id=None}", getHead, false},
	{"/get-note/{field}/{item_id}/{library_id=None}", getHead, false},
	{"/get-note-from-item-val/{field}/{item}/{library_id=None}", getHead, false},
	{"/get-note-resource/{scheme}/{digest}/{library_id=None}", getHead, false},
	{"/set-note/{field}/{item_id}/{library_id=None}", postOnly, true},
	{"/data-files/get/{book_id}/{relpath}/{library_id=None}", getHead, false},
	{"/data-files/upload/{book_id}/{library_id=None}", postOnly, true},
	{"/data-files/remove/{book_id}/{library_id=None}", postOnly, true},
	{"/conversion/start/{book_id}", postOnly, true},
	{"/conversion/status/{job_id}", postOnly, true},
	{"/conversion/book-data/{book_id}", getHead, false},
	{"/fts/search", getHead, false},
	{"/fts/disable", postOnly, true},
	{"/fts/reindex", postOnly, true},
	{"/fts/indexing", postOnly, true},
	{"/fts/snippets/{book_ids}", getHead, false},
	{"/kosync/healthcheck", getHead, false},
	{"/kosync/users/create", postOnly, false},
	{"/kosync/users/auth", getHead, false},
	{`/kosync/syncs/progress/{document=""}`, getHeadPut, false},
	{"/mobile", getHead, false},
	{`/browse/{+rest=""}`, getHead, false},
	{`/stanza/{+rest=""}`, getHead, false},
	{`/legacy/get/{what}/{book_id}/{library_id}/{+filename=""}`, getHead, false},
	{"/legacy/book/{book_id}/{library_id}", getHead, false},
	{"/metadata-download/start/{book_id}/{library_id=None}", postOnly, true},
	{"/metadata-download/start-covers/{book_id}/{library_id=None}", postOnly, true},
	{"/metadata-download/status/{job_id}/{library_id=None}", postOnly, true},
	{"/metadata-download/cover/{job_id}/{num}/{library_id=None}", getHead, true},
	{"/opds", getHead, false},
	{"/opds/navcatalog/{which}", getHead, false},
	{"/opds/category/{category}/{which}", getHead, false},
	{"/opds/categorygroup/{category}/{which}", getHead, false},
	{"/opds/fts", getHead, false},
	{`/opds/search/{query=""}`, getHead, false},
	{"/users/change-pw", postOnly, false},
}

// readOnlyCommands are the calibredb commands that calibre-server runs at
// /cdb/cmd/{which} without write access: those whose module in
// src/calibre/db/cli sets readonly = True. Each changes nothing in the
// library; backup_metadata and catalog refuse to run there at all.
var readOnlyCommands = []string{
	"backup_metadata", "catalog", "clone", "custom_columns", "export",
	"fts_search", "list", "list_categories", "search", "show_metadata",
}

// csMatcher is a route, split into the parts calibre matches it by.
type csMatcher struct {
	csRoute
	literal []string // each part: its text, or "" for a variable
	min     int      // the fewest parts a path may have
	soak    bool     // the last variable takes the rest of the path
}

var contentServerMatchers = compileContentServer(contentServerRoutes)

// compileContentServer splits each route as calibre's Route does: a part
// in braces is a variable; one with "=" has a default, and it and those
// after it may be left out; one starting with "+" takes the rest of the
// path.
func compileContentServer(routes []csRoute) []csMatcher {
	out := make([]csMatcher, 0, len(routes))
	for _, r := range routes {
		m := csMatcher{csRoute: r, min: -1}
		for i, p := range components(r.route) {
			if !strings.HasPrefix(p, "{") {
				m.literal = append(m.literal, p)
				continue
			}
			m.literal = append(m.literal, "")
			name := strings.Trim(p, "{}")
			if strings.HasPrefix(name, "+") {
				m.soak = true
			}
			if strings.Contains(name, "=") && m.min < 0 {
				m.min = i
			}
		}
		if m.min < 0 {
			m.min = len(m.literal)
		}
		out = append(out, m)
	}
	return out
}

// components splits a path the way calibre does: at each slash, leaving
// out the empty parts.
func components(path string) []string {
	return slices.DeleteFunc(strings.Split(path, "/"), func(s string) bool { return s == "" })
}

// matches reports whether m takes a path of these parts.
func (m *csMatcher) matches(parts []string) bool {
	if len(parts) < m.min || (!m.soak && len(parts) > len(m.literal)) {
		return false
	}
	for i, lit := range m.literal {
		if i >= len(parts) {
			break
		}
		if lit != "" && parts[i] != lit {
			return false
		}
	}
	return true
}

func contentServerMutating(method, path string) bool {
	parts := components(path)
	for i := range contentServerMatchers {
		m := &contentServerMatchers[i]
		if !m.matches(parts) {
			continue
		}
		switch {
		case !slices.Contains(m.methods, method):
			return true
		case m.route == cdbCommands:
			return !slices.Contains(readOnlyCommands, parts[2])
		default:
			return m.writes
		}
	}
	return true
}

// cwRoute is a Calibre-Web route, written as Flask writes it, with the
// blueprint's prefix in front, and the methods it is listed for.
type cwRoute struct {
	methods []string
	route   string
}

// calibreWebReadOnly lists the routes of Calibre-Web, at the commit the
// image builds, that take a method other than GET and HEAD but write
// nothing to the library: from the route decorators in cps/*.py. Most of
// them change Calibre-Web's own settings, users, shelves or reading state,
// which live in its app.db. A request to send a book to an e-reader is here
// only without a conversion: with one, the new format is written to the
// library.
var calibreWebReadOnly = []cwRoute{
	{postOnly, "/shutdown"},
	{postOnly, "/ajax/updateThumbnails"},
	{postOnly, "/admin/ajaxconfig"},
	{postOnly, "/ajax/deleteuser"},
	{postOnly, "/ajax/editlistusers/<param>"},
	{postOnly, "/ajax/user_table_settings"},
	{postOnly, "/admin/viewconfig"},
	{postOnly, "/ajax/loaddialogtexts/<element_id>"},
	{postOnly, "/ajax/editdomain/<int:allow>"},
	{postOnly, "/ajax/adddomain/<int:allow>"},
	{postOnly, "/ajax/deletedomain"},
	{postOnly, "/ajax/editrestriction/<int:res_type>"},
	{postOnly, "/ajax/editrestriction/<int:res_type>/<int:user_id>"},
	{postOnly, "/ajax/addrestriction/<int:res_type>"},
	{postOnly, "/ajax/addrestriction/<int:res_type>/<int:user_id>"},
	{postOnly, "/ajax/deleterestriction/<int:res_type>"},
	{postOnly, "/ajax/deleterestriction/<int:res_type>/<int:user_id>"},
	{postOnly, "/ajax/fullsync"},
	{postOnly, "/ajax/fullsync/<int:userid>"},
	{postOnly, "/ajax/simulatedbchange"},
	{postOnly, "/admin/user/new"},
	{postOnly, "/admin/mailsettings"},
	{postOnly, "/admin/scheduledtasks"},
	{postOnly, "/admin/user/<int:user_id>"},
	{postOnly, "/admin/resetpassword/<int:user_id>"},
	{postOnly, "/get_updater_status"},
	{postOnly, "/import_ldap_users"},
	{postOnly, "/ajax/canceltask"},
	{postOnly, "/ajax/simulatemerge"},
	{postOnly, "/ajax/displayselectedbooks"},
	{postOnly, "/ajax/archiveselectedbooks"},
	{[]string{http.MethodPost, http.MethodDelete}, "/kobo/<auth_token>/v1/library/tags"},
	{[]string{http.MethodDelete, http.MethodPut}, "/kobo/<auth_token>/v1/library/tags/<tag_id>"},
	{postOnly, "/kobo/<auth_token>/v1/library/tags/<tag_id>/items"},
	{postOnly, "/kobo/<auth_token>/v1/library/tags/<tag_id>/items/delete"},
	{[]string{http.MethodPut}, "/kobo/<auth_token>/v1/library/<book_uuid>/state"},
	{[]string{http.MethodDelete}, "/kobo/<auth_token>/v1/library/<book_uuid>"},
	{[]string{http.MethodDelete, http.MethodPost}, "/kobo/<auth_token>/v1/library/<dummy>"},
	{postOnly, "/kobo/<auth_token>/v1/library/<dummy>/preview"},
	{postOnly, "/kobo/<auth_token>/v1/user/loyalty/<dummy>"},
	{postOnly, "/kobo/<auth_token>/v1/user/profile"},
	{postOnly, "/kobo/<auth_token>/v1/user/wishlist"},
	{postOnly, "/kobo/<auth_token>/v1/user/recommendations"},
	{postOnly, "/kobo/<auth_token>/v1/analytics/<dummy>"},
	{postOnly, "/kobo/<auth_token>/v1/analytics/gettests"},
	{postOnly, "/kobo/<auth_token>/v1/products/<dummy>/prices"},
	{postOnly, "/kobo/<auth_token>/v1/products/<dummy>/recommendations"},
	{postOnly, "/kobo/<auth_token>/v1/products/<dummy>/nextread"},
	{postOnly, "/kobo/<auth_token>/v1/products/<dummy>/reviews"},
	{postOnly, "/kobo/<auth_token>/v1/products/featured/<dummy>"},
	{postOnly, "/kobo/<auth_token>/v1/products/featured/"},
	{postOnly, "/kobo/<auth_token>/v1/products/books/external/<dummy>"},
	{postOnly, "/kobo/<auth_token>/v1/products/books/series/<dummy>"},
	{postOnly, "/kobo/<auth_token>/v1/products/books/<dummy>"},
	{postOnly, "/kobo/<auth_token>/v1/products/books/<dummy>/"},
	{postOnly, "/kobo/<auth_token>/v1/products/dailydeal"},
	{postOnly, "/kobo/<auth_token>/v1/products/deals"},
	{postOnly, "/kobo/<auth_token>/v1/products"},
	{postOnly, "/kobo/<auth_token>/v1/products/<path:dummy>"},
	{postOnly, "/kobo/<auth_token>/v1/products/<path:dummy>/"},
	{postOnly, "/kobo/<auth_token>/v1/affiliate"},
	{postOnly, "/kobo/<auth_token>/v1/deals"},
	{postOnly, "/kobo/<auth_token>/v1/auth/refresh"},
	{postOnly, "/kobo/<auth_token>/v1/auth/device"},
	{postOnly, "/kobo_auth/deleteauthtoken/<int:user_id>"},
	{postOnly, "/verify/<token>"},
	{postOnly, "/ajax/verify_token"},
	{postOnly, "/advsearch"},
	{postOnly, "/metadata/provider"},
	{postOnly, "/metadata/provider/<prov_name>"},
	{postOnly, "/metadata/search"},
	{postOnly, "/shelf/add/<int:shelf_id>/<int:book_id>"},
	{postOnly, "/shelf/massremove/<int:shelf_id>"},
	{postOnly, "/shelf/massadd/<int:shelf_id>"},
	{postOnly, "/shelf/remove/<int:shelf_id>/<int:book_id>"},
	{postOnly, "/shelf/create"},
	{postOnly, "/shelf/edit/<int:shelf_id>"},
	{postOnly, "/shelf/delete/<int:shelf_id>"},
	{postOnly, "/shelf/order/<int:shelf_id>"},
	{postOnly, "/ajax/bookmark/<int:book_id>/<book_format>"},
	{postOnly, "/ajax/togglearchived/<int:book_id>"},
	{postOnly, "/ajax/view"},
	{postOnly, "/ajax/table_settings"},
	{postOnly, "/send/<int:book_id>/<book_format>/0"},
	{postOnly, "/register"},
	{postOnly, "/login"},
	{postOnly, "/me"},
}

// calibreWebGetWrites lists the GET routes of Calibre-Web that write to
// the library: Google Drive's change notice replaces metadata.db, and a
// Kobo sync, or setting up a Kobo, converts each EPUB that has no KEPUB yet
// and adds the KEPUB to the library.
var calibreWebGetWrites = []cwRoute{
	{getHead, "/gdrive/watch/callback"},
	{getHead, "/kobo/<auth_token>/v1/library/sync"},
	{getHead, "/kobo_auth/generate_auth_token/<int:user_id>"},
}

func calibreWebMutating(method, path string) bool {
	if method == http.MethodGet || method == http.MethodHead {
		return matchesAny(calibreWebGetWrites, method, path)
	}
	return !matchesAny(calibreWebReadOnly, method, path)
}

// matchesAny reports whether a route in routes takes method on path.
func matchesAny(routes []cwRoute, method, path string) bool {
	for _, r := range routes {
		if slices.Contains(r.methods, method) && flaskMatches(r.route, path) {
			return true
		}
	}
	return false
}

// flaskMatches reports whether a Flask route takes path, part by part: a
// literal part must be the same, <int:…> takes digits, <path:…> takes the
// rest of the path, and any other variable takes one part that is not
// empty. A trailing slash is a part of its own, as it is to Flask.
func flaskMatches(route, path string) bool {
	want, got := strings.Split(route, "/"), strings.Split(path, "/")
	for i, w := range want {
		if i >= len(got) {
			return false
		}
		switch {
		case strings.HasPrefix(w, "<path:"):
			return got[i] != ""
		case strings.HasPrefix(w, "<int:"):
			if got[i] == "" || strings.Trim(got[i], "0123456789") != "" {
				return false
			}
		case strings.HasPrefix(w, "<"):
			if got[i] == "" {
				return false
			}
		case w != got[i]:
			return false
		}
	}
	return len(want) == len(got)
}
