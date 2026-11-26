// SPDX-License-Identifier: GPL-3.0-only
// SPDX-FileCopyrightText: 2026 Aryan Ameri <github@aryan.ameri.coffee>

package reload

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
)

// readLimit is the most of an answer's body the Coordinator reads, once
// it is unpacked: as much as the proxy keeps of it.
const readLimit = 64 << 10

// conversionRequest says which of calibre-server's conversion requests
// path is: "start" for /conversion/start/{book_id}, "status" for
// /conversion/status/{job_id}, or "" for neither. calibre takes them as
// POSTs alone (src/calibre/srv/convert.py).
func conversionRequest(method, path string) (kind, id string) {
	parts := components(path)
	if method != http.MethodPost || len(parts) != 3 || parts[0] != "conversion" {
		return "", ""
	}
	if parts[1] == "start" || parts[1] == "status" {
		return parts[1], parts[2]
	}
	return "", ""
}

// Watched reports whether the Coordinator reads calibre-server's answer to
// a request: the start of a conversion, or a conversion's status.
func Watched(method, path string) bool {
	kind, _ := conversionRequest(method, path)
	return kind != ""
}

// Answered tells the Coordinator what calibre-server answered to a request
// Watched picked: the status, the header, as much of the body as the proxy
// kept, and whether that is the whole body.
//
// A start that calibre-server took answers with the job's id. When the id
// cannot be read, the conversion counts as running for Cap. A status that
// says "running": false ends the job: calibre-server answers it once,
// before anything else, and writes the new format to the library while it
// does.
func (c *Coordinator) Answered(method, path string, status int, header http.Header, body []byte, whole bool) {
	kind, id := conversionRequest(method, path)
	if kind == "" || status < 200 || status > 299 {
		return
	}
	if kind == "start" {
		if jobID, ok := readJobID(header, body, whole); ok {
			c.ConversionStarted(jobID)
		} else {
			c.conversionUnread()
		}
		return
	}
	jobID, err := strconv.Atoi(id)
	if err == nil && stoppedRunning(header, body) {
		c.ConversionDone(jobID)
	}
}

// unpacked returns a reader of body as the app wrote it, when calibre's
// one encoding, gzip, or none was used.
func unpacked(header http.Header, body []byte) (io.Reader, bool) {
	switch strings.ToLower(strings.TrimSpace(header.Get("Content-Encoding"))) {
	case "", "identity":
		return io.LimitReader(bytes.NewReader(body), readLimit), true
	case "gzip", "x-gzip":
		zr, err := gzip.NewReader(bytes.NewReader(body))
		if err != nil {
			return nil, false
		}
		return io.LimitReader(zr, readLimit), true
	}
	return nil, false
}

// readJobID reads the answer to a start: the job's id, as a JSON number.
func readJobID(header http.Header, body []byte, whole bool) (int, bool) {
	r, ok := unpacked(header, body)
	if !ok || !whole {
		return 0, false
	}
	b, err := io.ReadAll(r)
	if err != nil {
		return 0, false
	}
	var id *int
	if err := json.Unmarshal(b, &id); err != nil || id == nil {
		return 0, false
	}
	return *id, true
}

// stoppedRunning reports whether the answer to a status poll is a JSON
// object whose "running" is false. It reads the object a field at a time,
// so the start of a long answer is enough.
func stoppedRunning(header http.Header, body []byte) bool {
	r, ok := unpacked(header, body)
	if !ok {
		return false
	}
	dec := json.NewDecoder(r)
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return false
	}
	for dec.More() {
		key, err := dec.Token()
		if err != nil {
			return false
		}
		if key == "running" {
			v, err := dec.Token()
			return err == nil && v == false
		}
		var skip json.RawMessage
		if err := dec.Decode(&skip); err != nil {
			return false
		}
	}
	return false
}
