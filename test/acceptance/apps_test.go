// SPDX-License-Identifier: GPL-3.0-only
// SPDX-FileCopyrightText: 2026 Aryan Ameri <github@aryan.ameri.coffee>

package acceptance

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"image"
	_ "image/jpeg" // the covers both apps serve
	_ "image/png"  // the covers a test sets
	"io"
	"maps"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/divbast/ci/harness"
)

// libraryID is the library's id in the Content Server's routes.
const libraryID = "books"

// bigClient sends a request whose body or answer can be large, such as an
// upload of hundreds of megabytes, which HostClient's 30 s would cut off.
func bigClient() *http.Client {
	c := harness.HostClient()
	c.Timeout = 10 * time.Minute
	return c
}

// server is admin's client of the Content Server, through port 8080.
type server struct {
	s  *harness.Serving
	pw string
}

// challenge reads the Content Server's digest challenge from a request
// the image never refuses while a restart is to come, and which can change
// nothing: so it does not count as a change, for PID 1, nor is it refused.
// A 503, during a restart, is returned as the error's answer.
func (c *server) challenge(ctx context.Context) (string, harness.Response, error) {
	resp, err := c.s.Get(ctx, "/ajax/library-info", nil)
	if err != nil {
		return "", resp, err
	}
	if resp.Status != http.StatusUnauthorized {
		return "", resp, fmt.Errorf("the Content Server answered %d to a request with no login, not 401", resp.Status)
	}
	return resp.Header.Get("WWW-Authenticate"), resp, nil
}

// authorize returns header with admin's digest answer for method path, to a
// new challenge. A challenge that is not 401 is returned as the answer.
func (c *server) authorize(ctx context.Context, method, path string, header http.Header) (http.Header, harness.Response, error) {
	ch, resp, err := c.challenge(ctx)
	if err != nil {
		return nil, resp, err
	}
	auth, err := digestAnswer(ch, method, path, admin, c.pw)
	if err != nil {
		return nil, resp, err
	}
	h := header.Clone()
	if h == nil {
		h = http.Header{}
	}
	h.Set("Authorization", auth)
	return h, harness.Response{}, nil
}

// do sends method path to the Content Server as admin, with header and
// body. When the challenge itself is refused, as every request is during a
// restart, that answer is returned, with no error.
func (c *server) do(ctx context.Context, method, path string, header http.Header, body []byte) (harness.Response, error) {
	h, refused, err := c.authorize(ctx, method, path, header)
	if err != nil {
		if refused.Status == http.StatusServiceUnavailable {
			return refused, nil
		}
		return refused, err
	}
	return c.s.Do(ctx, nil, method, path, h, body)
}

// postJSON sends v to path as JSON, as admin, and reads the answer into
// out when it is 200.
func (c *server) postJSON(ctx context.Context, path string, v, out any) (harness.Response, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return harness.Response{}, err
	}
	resp, err := c.do(ctx, http.MethodPost, path, http.Header{"Content-Type": {"application/json"}}, b)
	if err != nil || resp.Status != http.StatusOK || out == nil {
		return resp, err
	}
	if err := json.Unmarshal(resp.Body, out); err != nil {
		return resp, fmt.Errorf("reading the answer to POST %s: %w: %s", path, err, harness.Clip(string(resp.Body)))
	}
	return resp, nil
}

// serverBook is a book as the Content Server's /ajax/book gives it.
type serverBook struct {
	Title   string   `json:"title"`
	Formats []string `json:"formats"`
}

// book returns book id as the Content Server shows it, and the answer's
// status: 404 for a book it does not have.
func (c *server) book(ctx context.Context, id int) (serverBook, int, error) {
	resp, err := c.do(ctx, http.MethodGet, fmt.Sprintf("/ajax/book/%d/%s", id, libraryID), nil, nil)
	var b serverBook
	if err != nil || resp.Status != http.StatusOK {
		return b, resp.Status, err
	}
	if err := json.Unmarshal(resp.Body, &b); err != nil {
		return b, resp.Status, fmt.Errorf("reading book %d from the Content Server: %w", id, err)
	}
	return b, resp.Status, nil
}

// setTitle sets book id's title through the Content Server, as its own
// edit page does.
func (c *server) setTitle(ctx context.Context, id int, title string) (harness.Response, error) {
	return c.postJSON(ctx, fmt.Sprintf("/cdb/set-fields/%d/%s", id, libraryID),
		map[string]any{"changes": map[string]string{"title": title}, "loaded_book_ids": []int{id}}, nil)
}

// addBook adds data, a book file called name, through the Content Server,
// as its own "Add books" does, and returns the new book's id.
func (c *server) addBook(ctx context.Context, name string, data []byte) (int, error) {
	resp, err := c.do(ctx, http.MethodPost, "/cdb/add-book/1/y/"+name+"/"+libraryID, nil, data)
	return addedID(resp, err)
}

// addedID reads the new book's id from the Content Server's answer to an
// add.
func addedID(resp harness.Response, err error) (int, error) {
	if err != nil {
		return 0, err
	}
	var added struct {
		ID int `json:"book_id"`
	}
	if resp.Status != http.StatusOK || json.Unmarshal(resp.Body, &added) != nil || added.ID == 0 {
		return 0, fmt.Errorf("the Content Server answered the add %d: %s", resp.Status, harness.Clip(string(resp.Body)))
	}
	return added.ID, nil
}

// deleteBook deletes book id through the Content Server.
func (c *server) deleteBook(ctx context.Context, id int) error {
	resp, err := c.do(ctx, http.MethodPost, fmt.Sprintf("/cdb/delete-books/%d/%s", id, libraryID), nil, nil)
	return answered("deleting a book in the Content Server", resp, err)
}

// setCover gives book id the cover img through the Content Server.
func (c *server) setCover(ctx context.Context, id int, img []byte) error {
	resp, err := c.do(ctx, http.MethodPost, fmt.Sprintf("/cdb/set-cover/%d/%s", id, libraryID), nil, img)
	return answered("setting a cover in the Content Server", resp, err)
}

// cover returns book id's cover as the Content Server serves it.
func (c *server) cover(ctx context.Context, id int) ([]byte, error) {
	resp, err := c.do(ctx, http.MethodGet, fmt.Sprintf("/get/cover/%d/%s", id, libraryID), nil, nil)
	err = answered("reading a cover from the Content Server", resp, err)
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}

// startConversion starts converting book id from one format to another in
// the Content Server, as its own Convert page does, and returns the job.
func (c *server) startConversion(ctx context.Context, id int, from, to string) (int, error) {
	var job int
	resp, err := c.postJSON(ctx, fmt.Sprintf("/conversion/start/%d", id),
		map[string]any{"input_fmt": from, "output_fmt": to, "options": map[string]any{}}, &job)
	if err == nil && resp.Status != http.StatusOK {
		err = fmt.Errorf("the Content Server answered %d: %s", resp.Status, harness.Clip(string(resp.Body)))
	}
	if err != nil {
		return 0, fmt.Errorf("starting a conversion in the Content Server: %w", err)
	}
	return job, nil
}

// conversion is the Content Server's answer to a conversion's status poll.
// A failed conversion's Log is its worker's output, and Traceback is the
// error calibre-server caught from it.
type conversion struct {
	Running   bool   `json:"running"`
	OK        bool   `json:"ok"`
	Log       string `json:"log"`
	Traceback string `json:"traceback"`
	Fmt       string `json:"fmt"`
}

// poll asks once how job runs, as the Content Server's page does.
func (c *server) poll(ctx context.Context, job int) (conversion, error) {
	var st conversion
	resp, err := c.postJSON(ctx, fmt.Sprintf("/conversion/status/%d", job), map[string]any{}, &st)
	if err == nil && resp.Status != http.StatusOK {
		err = fmt.Errorf("the Content Server answered %d: %s", resp.Status, harness.Clip(string(resp.Body)))
	}
	return st, err
}

// finish polls job every second, as the Content Server's page does, until
// it no longer runs, for at most within, and returns its last answer.
func (c *server) finish(ctx context.Context, job int, within time.Duration) (conversion, error) {
	var st conversion
	err := harness.Poll(ctx, within, time.Second, func(ctx context.Context) error {
		var err error
		if st, err = c.poll(ctx, job); err != nil {
			return err
		}
		if st.Running {
			return harness.NotYet(fmt.Errorf("conversion %d still runs", job))
		}
		return nil
	})
	return st, err
}

// convert converts book id in the Content Server, start to finish, and
// fails unless the conversion worked.
func (c *server) convert(ctx context.Context, id int, from, to string, within time.Duration) (conversion, error) {
	job, err := c.startConversion(ctx, id, from, to)
	if err != nil {
		return conversion{}, err
	}
	st, err := c.finish(ctx, job, within)
	if err == nil && !st.OK {
		err = fmt.Errorf("the Content Server's conversion of book %d to %s failed: %s", id, to, harness.Clip(st.Log))
	}
	return st, err
}

// answered is an error unless resp is a 200.
func answered(what string, resp harness.Response, err error) error {
	switch {
	case err != nil:
		return fmt.Errorf("%s: %w", what, err)
	case resp.Status != http.StatusOK:
		return fmt.Errorf("%s: answered %d: %s", what, resp.Status, harness.Clip(string(resp.Body)))
	}
	return nil
}

// bookPage returns Calibre-Web's page of book id: 200 for a book it has,
// and a 302 to its start page for one it does not.
func (w *webSession) bookPage(ctx context.Context, id int) (harness.Response, error) {
	return w.get(ctx, fmt.Sprintf("/book/%d", id), nil)
}

// shows reports whether Calibre-Web's page of book id shows its title as
// title: the page holds it, as the page escapes it.
func (w *webSession) shows(ctx context.Context, id int, title string) (bool, error) {
	resp, err := w.bookPage(ctx, id)
	if err != nil || resp.Status != http.StatusOK {
		return false, err
	}
	return bytes.Contains(resp.Body, []byte(html.EscapeString(title))), nil
}

// hasFormat reports whether Calibre-Web offers book id in format, a link
// to its download.
func (w *webSession) hasFormat(ctx context.Context, id int, format string) (bool, error) {
	resp, err := w.bookPage(ctx, id)
	if err != nil || resp.Status != http.StatusOK {
		return false, err
	}
	return bytes.Contains(resp.Body, fmt.Appendf(nil, "/download/%d/%s/", id, strings.ToLower(format))), nil
}

// part is one file of a multipart form, application/octet-stream unless
// it says its type.
type part struct {
	field, name, contentType string
	data                     io.Reader
	size                     int64
}

// multipartBody is form, then the files, as a browser sends them, and the
// body's length and content type: written as it is read, so a large file
// is never held in memory.
func multipartBody(form url.Values, files ...part) (body io.Reader, size int64, contentType string, err error) {
	var head bytes.Buffer
	mw := multipart.NewWriter(&head)
	for name, values := range form {
		for _, v := range values {
			if err := mw.WriteField(name, v); err != nil {
				return nil, 0, "", err
			}
		}
	}
	// Each part after the first opens with a line break before its
	// boundary, as multipart.Writer writes it; so does the closing one
	readers := make([]io.Reader, 0, 2*len(files)+2)
	readers = append(readers, bytes.NewReader(slices.Clone(head.Bytes())))
	size = int64(head.Len())
	opened := head.Len() > 0
	for _, f := range files {
		var ph bytes.Buffer
		if opened {
			ph.WriteString("\r\n")
		}
		opened = true
		h := textproto.MIMEHeader{}
		h.Set("Content-Disposition", fmt.Sprintf(`form-data; name=%q; filename=%q`, f.field, f.name))
		h.Set("Content-Type", cmp.Or(f.contentType, "application/octet-stream"))
		ph.WriteString("--" + mw.Boundary() + "\r\n")
		for k, vs := range h {
			for _, v := range vs {
				ph.WriteString(k + ": " + v + "\r\n")
			}
		}
		ph.WriteString("\r\n")
		readers = append(readers, bytes.NewReader(ph.Bytes()), f.data)
		size += int64(ph.Len()) + f.size
	}
	tail := "--" + mw.Boundary() + "--\r\n"
	if opened {
		tail = "\r\n" + tail
	}
	readers = append(readers, strings.NewReader(tail))
	size += int64(len(tail))
	return io.MultiReader(readers...), size, mw.FormDataContentType(), nil
}

// send sends method path to base with client, and a body of size bytes
// read from body, and reads the whole answer.
func send(ctx context.Context, client *http.Client, base, method, path string, header http.Header, body io.Reader, size int64) (harness.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, base+path, body)
	if err != nil {
		return harness.Response{}, err
	}
	req.ContentLength = size
	maps.Copy(req.Header, header)
	resp, err := client.Do(req)
	if err != nil {
		return harness.Response{}, err
	}
	b, err := io.ReadAll(resp.Body)
	if err = errors.Join(err, resp.Body.Close()); err != nil {
		return harness.Response{}, fmt.Errorf("%s %s: reading the answer: %w", method, path, err)
	}
	return harness.Response{Status: resp.StatusCode, Header: resp.Header, Body: b}, nil
}

// postFiles posts form and files to path as a multipart form, with the
// session's CSRF token, as Calibre-Web's own pages do.
func (w *webSession) postFiles(ctx context.Context, client *http.Client, path string, form url.Values, files ...part) (harness.Response, error) {
	f := url.Values{}
	maps.Copy(f, form)
	f.Set("csrf_token", w.token)
	body, size, ct, err := multipartBody(f, files...)
	if err != nil {
		return harness.Response{}, err
	}
	client.Jar = w.client.Jar
	h := http.Header{"Content-Type": {ct}, "X-CSRFToken": {w.token}}
	return send(ctx, client, w.s.Base, http.MethodPost, path, h, body, size)
}

// uploadedAt is where Calibre-Web sends the uploader of a new book: its
// edit page.
var uploadedAt = regexp.MustCompile(`^/admin/book/(\d+)$`)

// upload uploads data, of size bytes, a book file called name, through
// Calibre-Web's Upload, and returns the new book's id.
func (w *webSession) upload(ctx context.Context, name string, data io.Reader, size int64) (int, error) {
	if _, err := w.get(ctx, "/", nil); err != nil {
		return 0, err
	}
	resp, err := w.postFiles(ctx, bigClient(), "/upload", nil, part{field: "btn-upload", name: name, data: data, size: size})
	if err != nil {
		return 0, fmt.Errorf("uploading %s to Calibre-Web: %w", name, err)
	}
	var to struct {
		Location string `json:"location"`
	}
	if resp.Status != http.StatusOK || json.Unmarshal(resp.Body, &to) != nil {
		return 0, fmt.Errorf("uploading %s to Calibre-Web: answered %d: %s", name, resp.Status, harness.Clip(string(resp.Body)))
	}
	m := uploadedAt.FindStringSubmatch(to.Location)
	if m == nil {
		return 0, fmt.Errorf("uploading %s to Calibre-Web: it sends the uploader to %q, no book's edit page", name, to.Location)
	}
	return strconv.Atoi(m[1])
}

// deleteBook deletes book id through Calibre-Web's edit page.
func (w *webSession) deleteBook(ctx context.Context, id int) error {
	if _, err := w.get(ctx, fmt.Sprintf("/admin/book/%d", id), nil); err != nil {
		return err
	}
	resp, err := w.post(ctx, fmt.Sprintf("/delete/%d", id), url.Values{}, nil)
	if err == nil && resp.Status != http.StatusFound && resp.Status != http.StatusOK {
		err = fmt.Errorf("answered %d: %s", resp.Status, harness.Clip(string(resp.Body)))
	}
	if err != nil {
		return fmt.Errorf("deleting book %d in Calibre-Web: %w", id, err)
	}
	return nil
}

// convert has Calibre-Web convert book id from one format to another, as
// its edit page's Convert does. Calibre-Web queues the conversion, and
// adds the new format once it is done.
func (w *webSession) convert(ctx context.Context, id int, from, to string) error {
	if _, err := w.get(ctx, fmt.Sprintf("/admin/book/%d", id), nil); err != nil {
		return err
	}
	resp, err := w.post(ctx, fmt.Sprintf("/admin/book/convert/%d", id), url.Values{"book_format_from": {from}, "book_format_to": {to}}, nil)
	if err == nil && resp.Status != http.StatusFound {
		err = fmt.Errorf("answered %d: %s", resp.Status, harness.Clip(string(resp.Body)))
	}
	if err != nil {
		return fmt.Errorf("converting book %d to %s in Calibre-Web: %w", id, to, err)
	}
	return nil
}

// setCover gives book id the cover img through Calibre-Web's edit page:
// the whole form as the page gives it, with the cover's file added.
func (w *webSession) setCover(ctx context.Context, id int, img []byte) error {
	page, err := w.get(ctx, fmt.Sprintf("/admin/book/%d", id), nil)
	if err != nil || page.Status != http.StatusOK {
		return fmt.Errorf("calibre-web's edit page of book %d: status %d: %w", id, page.Status, err)
	}
	form, err := formValues(page.Body, "title")
	if err != nil {
		return err
	}
	resp, err := w.postFiles(ctx, harness.HostClient(), fmt.Sprintf("/admin/book/%d", id), form,
		part{field: "btn-upload-cover", name: "cover.png", contentType: "image/png", data: bytes.NewReader(img), size: int64(len(img))})
	if err == nil && resp.Status != http.StatusFound && resp.Status != http.StatusOK {
		err = fmt.Errorf("answered %d: %s", resp.Status, harness.Clip(string(resp.Body)))
	}
	if err != nil {
		return fmt.Errorf("setting book %d's cover in Calibre-Web: %w", id, err)
	}
	// Calibre-Web sends the editor on whether or not it took the cover,
	// so its own view of the cover says which
	served, err := w.cover(ctx, id)
	want, werr := meanColour(img)
	if err = cmp.Or(werr, isColour(served, err, want)); err != nil {
		return fmt.Errorf("calibre-web did not take book %d's new cover: %w", id, err)
	}
	return nil
}

// cover returns book id's cover as Calibre-Web serves it.
func (w *webSession) cover(ctx context.Context, id int) ([]byte, error) {
	resp, err := w.get(ctx, fmt.Sprintf("/cover/%d", id), nil)
	err = answered("reading a cover from Calibre-Web", resp, err)
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}

// rgb is a colour, each channel from 0 to 255.
type rgb struct{ r, g, b int }

// meanColour returns the mean colour of img, a JPEG or a PNG.
func meanColour(img []byte) (rgb, error) {
	m, _, err := image.Decode(bytes.NewReader(img))
	if err != nil {
		return rgb{}, fmt.Errorf("reading a cover of %d bytes: %w", len(img), err)
	}
	var r, g, b, n uint64
	bounds := m.Bounds()
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			cr, cg, cb, _ := m.At(x, y).RGBA()
			r, g, b, n = r+uint64(cr>>8), g+uint64(cg>>8), b+uint64(cb>>8), n+1
		}
	}
	if n == 0 {
		return rgb{}, errors.New("the cover has no pixel")
	}
	return rgb{int(r / n), int(g / n), int(b / n)}, nil //nolint:gosec // G115: each mean is of values from 0 to 255
}

// near reports whether c is within 40 of want in each channel: a JPEG's
// colours move a little from what was drawn.
func (c rgb) near(want rgb) bool {
	return abs(c.r-want.r) <= 40 && abs(c.g-want.g) <= 40 && abs(c.b-want.b) <= 40
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
