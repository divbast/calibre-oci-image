// SPDX-License-Identifier: GPL-3.0-only
// SPDX-FileCopyrightText: 2026 Aryan Ameri <github@aryan.ameri.coffee>

package reload

import "testing"

// route is one row of the measured tables: a request, whether it can
// change the library, and the source line it was measured from.
type route struct {
	src          string
	method, path string
	want         bool
}

// contentServerRows is calibre-server 9.19.0's routes, from the @endpoint
// decorators in src/calibre/srv, each with a path it takes: true where
// the route's needs_db_write is set.
var contentServerRows = []route{
	{"ajax.py:179", "GET", "/ajax/book/12", false},
	{"ajax.py:179", "HEAD", "/ajax/book/12/books", false},
	{"ajax.py:225", "GET", "/ajax/books", false},
	{"ajax.py:285", "GET", "/ajax/categories/books", false},
	{"ajax.py:344", "GET", "/ajax/category/abc", false},
	{"ajax.py:508", "GET", "/ajax/books_in/abc/def", false},
	{"ajax.py:615", "GET", "/ajax/search", false},
	{"ajax.py:645", "GET", "/ajax/library-info", false},
	{"books.py:163", "GET", "/book-manifest/12/EPUB", false},
	{"books.py:207", "GET", "/book-file/12/EPUB/100/200/OEBPS/text/ch1.html", false},
	{"books.py:226", "GET", "/book-get-last-read-position/books/12-EPUB", false},
	{"books.py:251", "POST", "/book-set-last-read-position/books/12/EPUB", true},
	{"books.py:274", "GET", "/book-get-annotations/books/12-EPUB", false},
	{"books.py:300", "POST", "/book-update-annotations/books/12/EPUB", true},
	{"books.py:323", "GET", "/book-get-storage/books/12/EPUB", false},
	{"books.py:338", "POST", "/book-set-storage/books/12/EPUB", true},
	{"books.py:380", "GET", "/mathjax", false},
	{"books.py:380", "GET", "/mathjax/es5/tex-chtml.js", false},
	{"cdb.py:73", "POST", "/cdb/add-book/1/n/book.epub", true},
	{"cdb.py:129", "POST", "/cdb/delete-books/1,2/books", true},
	{"cdb.py:154", "POST", "/cdb/set-cover/12", true},
	{"cdb.py:186", "POST", "/cdb/set-fields/12/books", true},
	{"cdb.py:261", "POST", "/cdb/copy-to-library/other", true},
	{"code.py:54", "GET", "/", false},
	{"code.py:69", "GET", "/index.js.map", false},
	{"code.py:76", "GET", "/robots.txt", false},
	{"code.py:81", "GET", "/service_worker.js", false},
	{"code.py:87", "GET", "/manifest.json", false},
	{"code.py:119", "GET", "/ajax-setup", false},
	{"code.py:133", "POST", "/console-print", false},
	{"code.py:272", "GET", "/interface-data/update", false},
	{"code.py:272", "GET", "/interface-data/update/abc123", false},
	{"code.py:516", "GET", "/interface-data/books-init", false},
	{"code.py:535", "GET", "/interface-data/init", false},
	{"code.py:565", "GET", "/interface-data/newly-added", false},
	{"code.py:581", "POST", "/interface-data/more-books", false},
	{"code.py:581", "GET", "/interface-data/more-books", false},
	{"code.py:620", "POST", "/interface-data/set-session-data", false},
	{"code.py:638", "GET", "/interface-data/get-books", false},
	{"code.py:668", "GET", "/interface-data/book-metadata/12", false},
	{"code.py:689", "GET", "/web-search/12/authors/Someone", false},
	{"code.py:711", "GET", "/interface-data/tag-browser", false},
	{"code.py:731", "GET", "/interface-data/browse-field/tags", false},
	{"code.py:760", "GET", "/interface-data/field-names/tags", false},
	{"code.py:777", "GET", "/interface-data/field-id-map/tags", false},
	{"comic.py:164", "GET", "/comic-manifest/12", false},
	{"comic.py:203", "GET", "/comic-page/12/3", false},
	{"content.py:331", "GET", "/static/reader.html", false},
	{"content.py:346", "GET", "/favicon.png", false},
	{"content.py:351", "GET", "/favicon.svg", false},
	{"content.py:356", "GET", "/favicon-192.png", false},
	{"content.py:361", "GET", "/apple-touch-icon.png", false},
	{"content.py:366", "GET", "/icon/mimetypes/epub.png", false},
	{"content.py:415", "GET", "/reader-background/paper.jpg", false},
	{"content.py:428", "GET", "/reader-profiles/get-all", false},
	{"content.py:438", "POST", "/reader-profiles/save", false},
	{"content.py:456", "GET", "/get/EPUB/12/books", false},
	{"content.py:562", "GET", "/get-note/authors/3", false},
	{"content.py:577", "GET", "/get-note-from-item-val/authors/Someone", false},
	{"content.py:590", "GET", "/get-note-resource/xxh64/abcd", false},
	{"content.py:615", "POST", "/set-note/authors/3", true},
	{"content.py:697", "GET", "/data-files/get/12/data%2Fx.txt", false},
	{"content.py:713", "POST", "/data-files/upload/12", true},
	{"content.py:740", "POST", "/data-files/remove/12/books", true},
	{"convert.py:214", "POST", "/conversion/start/12", true},
	{"convert.py:253", "POST", "/conversion/status/5", true},
	{"convert.py:375", "GET", "/conversion/book-data/12", false},
	{"fts.py:33", "GET", "/fts/search", false},
	{"fts.py:89", "POST", "/fts/disable", true},
	{"fts.py:97", "POST", "/fts/reindex", true},
	{"fts.py:115", "POST", "/fts/indexing", true},
	{"fts.py:129", "GET", "/fts/snippets/1,2", false},
	{"kosync.py:136", "GET", "/kosync/healthcheck", false},
	{"kosync.py:141", "POST", "/kosync/users/create", false},
	{"kosync.py:147", "GET", "/kosync/users/auth", false},
	{"kosync.py:156", "GET", "/kosync/syncs/progress/abc", false},
	// It writes the reading position to the users' database, not the library
	{"kosync.py:156", "PUT", "/kosync/syncs/progress", false},
	{"legacy.py:280", "GET", "/mobile", false},
	{"legacy.py:320", "GET", "/browse/category/tags", false},
	{"legacy.py:347", "GET", "/stanza", false},
	{"legacy.py:352", "GET", "/legacy/get/EPUB/12/books/book.epub", false},
	{"legacy.py:366", "GET", "/legacy/book/12/books", false},
	// The flag is set, though a download writes nothing to the library:
	// what it finds is applied by /cdb/set-fields
	{"metadata_download.py:431", "POST", "/metadata-download/start/12", true},
	{"metadata_download.py:448", "POST", "/metadata-download/start-covers/12/books", true},
	{"metadata_download.py:490", "POST", "/metadata-download/status/7", true},
	{"metadata_download.py:551", "GET", "/metadata-download/cover/7/0", true},
	{"metadata_download.py:551", "HEAD", "/metadata-download/cover/7/0/books", true},
	{"opds.py:890", "GET", "/opds", false},
	{"opds.py:932", "GET", "/opds/navcatalog/4e6577", false},
	{"opds.py:955", "GET", "/opds/category/tags/abc", false},
	{"opds.py:1009", "GET", "/opds/categorygroup/tags/abc", false},
	{"opds.py:1062", "GET", "/opds/fts", false},
	{"opds.py:1085", "GET", "/opds/search/dune", false},
	{"users_api.py:18", "POST", "/users/change-pw", false},

	// /cdb/cmd/{which}/{version=0} (cdb.py:33): a command whose module sets
	// readonly = True (src/calibre/db/cli/cmd_*.py) is checked for no write
	// access, and the rest are
	{"cmd_backup_metadata.py:20", "POST", "/cdb/cmd/backup_metadata/0", false},
	{"cmd_catalog.py:22", "POST", "/cdb/cmd/catalog/0", false},
	{"cmd_clone.py:21", "POST", "/cdb/cmd/clone/0", false},
	{"cmd_custom_columns.py:20", "POST", "/cdb/cmd/custom_columns/0", false},
	{"cmd_export.py:26", "POST", "/cdb/cmd/export/0", false},
	{"cmd_fts_search.py:34", "POST", "/cdb/cmd/fts_search/0", false},
	{"cmd_list.py:33", "POST", "/cdb/cmd/list", false},
	{"cmd_list_categories.py:25", "POST", "/cdb/cmd/list_categories/0", false},
	{"cmd_search.py:18", "POST", "/cdb/cmd/search/0", false},
	{"cmd_show_metadata.py:21", "POST", "/cdb/cmd/show_metadata/0", false},
	{"cmd_add.py:34", "POST", "/cdb/cmd/add/0", true},
	{"cmd_add_custom_column.py:21", "POST", "/cdb/cmd/add_custom_column/0", true},
	{"cmd_add_format.py:21", "POST", "/cdb/cmd/add_format/0", true},
	{"cmd_annotations.py:23", "POST", "/cdb/cmd/annotations/1", true},
	{"cmd_check_library.py:26", "POST", "/cdb/cmd/check_library/0", true},
	{"cmd_data_files.py:36", "POST", "/cdb/cmd/data_files/0", true},
	{"cmd_embed_metadata.py:20", "POST", "/cdb/cmd/embed_metadata/0", true},
	{"cmd_fts_index.py (no readonly)", "POST", "/cdb/cmd/fts_index/0", true},
	{"cmd_items.py:20", "POST", "/cdb/cmd/items/0", true},
	{"cmd_notes.py:24", "POST", "/cdb/cmd/notes/0", true},
	{"cmd_remove.py:20", "POST", "/cdb/cmd/remove/0", true},
	{"cmd_remove_custom_column.py:21", "POST", "/cdb/cmd/remove_custom_column/0", true},
	{"cmd_remove_format.py:18", "POST", "/cdb/cmd/remove_format/0", true},
	{"cmd_restore_database.py:21", "POST", "/cdb/cmd/restore_database/0", true},
	{"cmd_saved_searches.py:9", "POST", "/cdb/cmd/saved_searches/0", true},
	{"cmd_set_custom.py:20", "POST", "/cdb/cmd/set_custom/0", true},
	{"cmd_set_metadata.py:25", "POST", "/cdb/cmd/set_metadata/0", true},
	{"cmd_trash.py:26", "POST", "/cdb/cmd/trash/0", true},
	{"cmd_virtual_libraries.py:21", "POST", "/cdb/cmd/virtual_libraries/0", true},
	{"cdb.py:37 (no such module)", "POST", "/cdb/cmd/no_such_command/0", true},

	// calibre leaves out the empty parts of a path
	{"http_request.py:113", "GET", "//ajax//library-info/", false},
	{"http_request.py:113", "POST", "/cdb//set-fields/12", true},

	// The defaults: a path no route takes, and a method its route does not
	// (routes.py:30, :379), count as writes
	{"routes.py:359", "GET", "/no/such/route", true},
	{"routes.py:359", "GET", "/ajax/library-info/extra", true},
	{"routes.py:379", "POST", "/ajax/library-info", true},
	{"routes.py:379", "HEAD", "/book-set-last-read-position/books/12/EPUB", true},
	{"routes.py:379", "GET", "/cdb/cmd/list/0", true},
	{"routes.py:379", "DELETE", "/kosync/syncs/progress/abc", true},
}

// calibreWebRows is Calibre-Web's routes at the commit the image builds,
// from the route decorators in cps/*.py: every route that takes a method
// other than GET and HEAD, the GET routes that write, and a few that do
// not.
var calibreWebRows = []route{
	// Writes nothing to the library
	{"admin.py:145", "POST", "/shutdown", false},
	{"admin.py:196", "POST", "/ajax/updateThumbnails", false},
	{"admin.py:261", "POST", "/admin/ajaxconfig", false},
	{"admin.py:391", "POST", "/ajax/deleteuser", false},
	{"admin.py:449", "POST", "/ajax/editlistusers/name", false},
	{"admin.py:554", "POST", "/ajax/user_table_settings", false},
	{"admin.py:571", "POST", "/admin/viewconfig", false},
	{"admin.py:620", "POST", "/ajax/loaddialogtexts/btndeluser", false},
	{"admin.py:662", "POST", "/ajax/editdomain/1", false},
	{"admin.py:676", "POST", "/ajax/adddomain/0", false},
	{"admin.py:690", "POST", "/ajax/deletedomain", false},
	{"admin.py:720", "POST", "/ajax/editrestriction/1", false},
	{"admin.py:721", "POST", "/ajax/editrestriction/1/2", false},
	{"admin.py:787", "POST", "/ajax/addrestriction/1", false},
	{"admin.py:794", "POST", "/ajax/addrestriction/1/2", false},
	{"admin.py:840", "POST", "/ajax/deleterestriction/1", false},
	{"admin.py:847", "POST", "/ajax/deleterestriction/1/2", false},
	{"admin.py:939", "POST", "/ajax/fullsync", false},
	{"admin.py:945", "POST", "/ajax/fullsync/3", false},
	{"admin.py:1300", "POST", "/ajax/simulatedbchange", false},
	{"admin.py:1308", "POST", "/admin/user/new", false},
	{"admin.py:1339", "POST", "/admin/mailsettings", false},
	{"admin.py:1417", "POST", "/admin/scheduledtasks", false},
	{"admin.py:1460", "POST", "/admin/user/3", false},
	{"admin.py:1489", "POST", "/admin/resetpassword/3", false},
	{"admin.py:1570", "POST", "/get_updater_status", false},
	{"admin.py:1665", "POST", "/import_ldap_users", false},
	{"admin.py:1720", "POST", "/ajax/canceltask", false},
	{"editbooks.py:493", "POST", "/ajax/simulatemerge", false},
	{"editbooks.py:508", "POST", "/ajax/displayselectedbooks", false},
	{"editbooks.py:520", "POST", "/ajax/archiveselectedbooks", false},
	{"kobo.py:507", "POST", "/kobo/tok/v1/library/tags", false},
	{"kobo.py:507", "DELETE", "/kobo/tok/v1/library/tags", false},
	{"kobo.py:542", "PUT", "/kobo/tok/v1/library/tags/t1", false},
	{"kobo.py:542", "DELETE", "/kobo/tok/v1/library/tags/t1", false},
	{"kobo.py:596", "POST", "/kobo/tok/v1/library/tags/t1/items", false},
	{"kobo.py:626", "POST", "/kobo/tok/v1/library/tags/t1/items/delete", false},
	{"kobo.py:759", "PUT", "/kobo/tok/v1/library/uuid-1/state", false},
	{"kobo.py:953", "DELETE", "/kobo/tok/v1/library/uuid-1", false},
	{"kobo.py:971", "POST", "/kobo/tok/v1/library/anything", false},
	{"kobo.py:972", "POST", "/kobo/tok/v1/library/anything/preview", false},
	{"kobo.py:982", "POST", "/kobo/tok/v1/user/loyalty/x", false},
	{"kobo.py:983", "POST", "/kobo/tok/v1/user/profile", false},
	{"kobo.py:984", "POST", "/kobo/tok/v1/user/wishlist", false},
	{"kobo.py:985", "POST", "/kobo/tok/v1/user/recommendations", false},
	{"kobo.py:986", "POST", "/kobo/tok/v1/analytics/event", false},
	{"kobo.py:1005", "POST", "/kobo/tok/v1/analytics/gettests", false},
	{"kobo.py:1016", "POST", "/kobo/tok/v1/products/p1/prices", false},
	{"kobo.py:1017", "POST", "/kobo/tok/v1/products/p1/recommendations", false},
	{"kobo.py:1018", "POST", "/kobo/tok/v1/products/p1/nextread", false},
	{"kobo.py:1019", "POST", "/kobo/tok/v1/products/p1/reviews", false},
	{"kobo.py:1020", "POST", "/kobo/tok/v1/products/featured/f1", false},
	{"kobo.py:1021", "POST", "/kobo/tok/v1/products/featured/", false},
	{"kobo.py:1022", "POST", "/kobo/tok/v1/products/books/external/b1", false},
	{"kobo.py:1023", "POST", "/kobo/tok/v1/products/books/series/s1", false},
	{"kobo.py:1024", "POST", "/kobo/tok/v1/products/books/b1", false},
	{"kobo.py:1025", "POST", "/kobo/tok/v1/products/books/b1/", false},
	{"kobo.py:1026", "POST", "/kobo/tok/v1/products/dailydeal", false},
	{"kobo.py:1027", "POST", "/kobo/tok/v1/products/deals", false},
	{"kobo.py:1028", "POST", "/kobo/tok/v1/products", false},
	{"kobo.py:1029", "POST", "/kobo/tok/v1/products/a/b/c", false},
	{"kobo.py:1030", "POST", "/kobo/tok/v1/products/a/b/c/", false},
	{"kobo.py:1031", "POST", "/kobo/tok/v1/affiliate", false},
	{"kobo.py:1032", "POST", "/kobo/tok/v1/deals", false},
	{"kobo.py:1060", "POST", "/kobo/tok/v1/auth/refresh", false},
	{"kobo.py:1061", "POST", "/kobo/tok/v1/auth/device", false},
	{"kobo_auth.py:125", "POST", "/kobo_auth/deleteauthtoken/3", false},
	{"remotelogin.py:90", "POST", "/verify/abc", false},
	{"remotelogin.py:117", "POST", "/ajax/verify_token", false},
	{"search.py:54", "POST", "/advsearch", false},
	{"search_metadata.py:127", "POST", "/metadata/provider", false},
	{"search_metadata.py:128", "POST", "/metadata/provider/google", false},
	{"search_metadata.py:153", "POST", "/metadata/search", false},
	{"shelf.py:41", "POST", "/shelf/add/1/12", false},
	{"shelf.py:105", "POST", "/shelf/massremove/1", false},
	{"shelf.py:151", "POST", "/shelf/massadd/1", false},
	{"shelf.py:201", "POST", "/shelf/remove/1/12", false},
	{"shelf.py:258", "POST", "/shelf/create", false},
	{"shelf.py:265", "POST", "/shelf/edit/1", false},
	{"shelf.py:275", "POST", "/shelf/delete/1", false},
	{"shelf.py:305", "POST", "/shelf/order/1", false},
	{"web.py:156", "POST", "/ajax/bookmark/12/epub", false},
	{"web.py:186", "POST", "/ajax/togglearchived/12", false},
	{"web.py:195", "POST", "/ajax/view", false},
	{"web.py:919", "POST", "/ajax/table_settings", false},
	{"web.py:1261 (helper.py:229, no conversion)", "POST", "/send/12/epub/0", false},
	{"web.py:1291", "POST", "/register", false},
	{"web.py:1390", "POST", "/login", false},
	{"web.py:1539", "POST", "/me", false},

	// Writes to the library, at once or in a task it starts
	{"admin.py:173 (helper.py:1160)", "POST", "/metadata_backup", true},
	{"admin.py:241 (admin.py:1766)", "POST", "/admin/dbconfig", true},
	{"admin.py:268", "POST", "/admin/ajaxdbconfig", true},
	{"editbooks.py:77", "POST", "/ajax/deletebook", true},
	{"editbooks.py:84", "POST", "/delete/12", true},
	{"editbooks.py:85", "POST", "/delete/12/epub", true},
	{"editbooks.py:98", "POST", "/admin/book/12", true},
	{"editbooks.py:105", "POST", "/upload", true},
	{"editbooks.py:176", "POST", "/admin/book/convert/12", true},
	{"editbooks.py:230", "POST", "/ajax/editbooks/title", true},
	{"editbooks.py:242", "POST", "/ajax/editselectedbooks", true},
	{"editbooks.py:536 (helper.py:340, :345)", "POST", "/ajax/readselectedbooks", true},
	{"editbooks.py:558", "POST", "/ajax/mergebooks", true},
	{"editbooks.py:596", "POST", "/ajax/xchange", true},
	{"gdrive.py:122", "POST", "/gdrive/watch/callback", true},
	{"web.py:176 (helper.py:340, :345)", "POST", "/ajax/toggleread/12", true},
	{"web.py:1261 (helper.py:222)", "POST", "/send/12/epub/1", true},
	{"web.py:1261 (helper.py:225)", "POST", "/send/12/epub/2", true},

	// GET routes that write
	{"gdrive.py:122 (gdrive.py:152)", "GET", "/gdrive/watch/callback", true},
	{"kobo.py:141 (kobo.py:213)", "GET", "/kobo/tok/v1/library/sync", true},
	{"kobo.py:141 (kobo.py:213)", "HEAD", "/kobo/tok/v1/library/sync", true},
	{"kobo_auth.py:81 (kobo_auth.py:115)", "GET", "/kobo_auth/generate_auth_token/3", true},

	// GET routes that do not
	{"web.py:806", "GET", "/", false},
	{"editbooks.py:91", "GET", "/admin/book/12", false},
	{"opds.py:438", "GET", "/opds/download/12/epub/", false},
	{"kobo.py:759", "GET", "/kobo/tok/v1/library/uuid-1/state", false},
	{"kobo.py:1147", "HEAD", "/kobo/tok/download/12/epub", false},

	// The default: any other method, and a listed route with the wrong
	// method, or a path Flask would not route to it, counts as a write
	{"default", "POST", "/no/such/route", true},
	{"default", "PUT", "/ajax/view", true},
	{"default", "POST", "/shelf/add/one/12", true},
	{"default", "POST", "/ajax/view/", true},
	{"default", "POST", "/send/12/epub/00", true},
	{"default", "OPTIONS", "/login", true},
}

// TestMutatingRoutes is the measured tables, row by row.
func TestMutatingRoutes(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		app  App
		rows []route
	}{{ContentServer, contentServerRows}, {CalibreWeb, calibreWebRows}} {
		for _, r := range tt.rows {
			if got := Mutating(tt.app, r.method, r.path); got != r.want {
				t.Errorf("Mutating(%s, %s, %q) = %t, want %t (%s)", tt.app, r.method, r.path, got, r.want, r.src)
			}
		}
	}
}

// TestContentServerRoutesOnce checks that a path is taken by one route at
// most, as calibre's routes are, so the order of the table does not
// matter.
func TestContentServerRoutesOnce(t *testing.T) {
	t.Parallel()
	for _, r := range contentServerRows {
		n := 0
		for i := range contentServerMatchers {
			if contentServerMatchers[i].matches(components(r.path)) {
				n++
			}
		}
		if n > 1 {
			t.Errorf("%q matches %d routes, want one at most", r.path, n)
		}
	}
}
