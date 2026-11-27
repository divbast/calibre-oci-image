// SPDX-License-Identifier: GPL-3.0-only
// SPDX-FileCopyrightText: 2026 Aryan Ameri <github@aryan.ameri.coffee>

package acceptance

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	byteorder "encoding/binary"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"image"
	"image/color"
	"image/png"
	"io"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/divbast/ci/harness"
)

// The fixture EPUB's metadata, as ebook-convert writes it into the file.
const (
	epubTitle  = "Divbast Fixture EPUB"
	epubAuthor = "Divbast Test"
)

// The colours of the fixture images: the PDF's page, the cover a test
// sets, and the comic's page.
var (
	pageRed   = rgb{220, 20, 20}
	coverBlue = rgb{20, 40, 220}
	comicGrn  = rgb{20, 200, 40}
)

// booksCode is Python that makes the fixture EPUB with the image's own
// Calibre, as uid 65532, from a text file, with the title sys.argv[1] and
// the author sys.argv[2], and prints it in base64.
const booksCode = `import base64, os, subprocess, sys
os.makedirs("/tmp/in")
env = dict(os.environ, CALIBRE_CONFIG_DIRECTORY="/tmp/calibre-config")
with open("/tmp/in/book.txt", "w") as f:
    f.write("A book the acceptance suite reads.\n\nIt has two paragraphs.\n")
subprocess.run(["/opt/calibre/ebook-convert", "/tmp/in/book.txt", "/tmp/in/book.epub", "--title", sys.argv[1], "--authors", sys.argv[2]],
               check=True, env=env, stdout=subprocess.DEVNULL)
print(base64.b64encode(open("/tmp/in/book.epub", "rb").read()).decode())
`

// fixtures are the books the suite uploads and converts, made once in a
// run: an EPUB, by the image's own Calibre, and a PDF of one page, red all
// over, so that its first page is nothing like Calibre-Web's generic
// cover. The suite writes the PDF itself, as it writes its large ones:
// Calibre's own PDF output draws pages with Chromium, which aborts under
// rootless Podman as the image is run there.
type fixtures struct {
	epub, pdf []byte
}

// made holds the fixtures once the first test has made them.
var made struct {
	once sync.Once
	f    *fixtures
	err  error
}

// fixtureBooks returns the fixture books, making them on the first call,
// in a one-off container of img under the contract, on no network.
func fixtureBooks(tb testing.TB, e harness.Engine, img string) *fixtures {
	tb.Helper()
	made.once.Do(func() {
		res, err := e.Run(tb.Context(), tb, &harness.RunSpec{
			Image:      img,
			Network:    "none",
			Confine:    harness.Contract("512m"),
			Entrypoint: []string{python},
			Args:       []string{"-I", "-c", booksCode, epubTitle, epubAuthor},
		})
		if err == nil && res.Code != 0 {
			err = fmt.Errorf("exit status %d: %s", res.Code, harness.Clip(string(res.Stderr)))
		}
		if err != nil {
			made.err = fmt.Errorf("making the fixture EPUB with the image's ebook-convert: %w", err)
			return
		}
		epub, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(res.Stdout)))
		if err != nil {
			made.err = fmt.Errorf("reading the fixture EPUB: %w", err)
			return
		}
		page, size := pdfPage(612, 792, pageRed, 0)
		pdf, err := io.ReadAll(page)
		if err == nil && int64(len(pdf)) != size {
			err = fmt.Errorf("the PDF is %d bytes, not %d", len(pdf), size)
		}
		if err != nil {
			made.err = fmt.Errorf("writing the fixture PDF: %w", err)
			return
		}
		made.f = &fixtures{epub: epub, pdf: pdf}
	})
	if made.err != nil {
		tb.Fatal(made.err)
	}
	return made.f
}

// solidPNG is a PNG of w by h pixels, all of colour c.
func solidPNG(w, h int, c rgb) []byte {
	m := image.NewRGBA(image.Rect(0, 0, w, h))
	fill := color.RGBA{R: uint8(c.r), G: uint8(c.g), B: uint8(c.b), A: 255} //nolint:gosec // G115: each channel is 0 to 255
	for y := range h {
		for x := range w {
			m.SetRGBA(x, y, fill)
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, m); err != nil {
		panic(err) // an in-memory PNG of a fixed size cannot fail
	}
	return b.Bytes()
}

// noisePNG is a PNG of w by h pixels of random colours, which does not
// compress: a page of a large comic.
func noisePNG(w, h int, seed uint64) []byte {
	r := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15)) //nolint:gosec // G404: test data, not a secret
	m := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := 0; i < len(m.Pix); i += 4 {
		v := r.Uint32()
		m.Pix[i], m.Pix[i+1], m.Pix[i+2], m.Pix[i+3] = uint8(v), uint8(v>>8), uint8(v>>16), 255 //nolint:gosec // G115: the low bytes, on purpose
	}
	var b bytes.Buffer
	if err := (&png.Encoder{CompressionLevel: png.BestSpeed}).Encode(&b, m); err != nil {
		panic(err)
	}
	return b.Bytes()
}

// hostDir is a new directory on the host, which uid 65532 may enter,
// holding files, each by its name, readable by all: for a read-only bind
// mount into a container.
func hostDir(tb testing.TB, files map[string][]byte) string {
	tb.Helper()
	dir := tb.TempDir()
	if err := os.Chmod(dir, 0o755); err != nil { //nolint:gosec // G302: uid 65532 enters it, through a read-only bind mount
		tb.Fatal(err)
	}
	for name, data := range files {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, data, 0o644); err != nil { //nolint:gosec // G306: uid 65532 reads it, through a read-only bind mount
			tb.Fatal(err)
		}
	}
	return dir
}

// fixturesDir is where a set-up container finds the fixture books.
const fixturesDir = "/fixtures"

// shelf is one book a fixture library holds: its title and author.
type shelf struct {
	Title  string `json:"title"`
	Author string `json:"author"`
}

// addCode is Python that adds the books /fixtures/books.json lists, each
// as a copy of /fixtures/book.epub, to the library in /books, in one run
// of the bundle's calibre-debug, through Calibre's own library code, as a
// desktop Calibre adds them; then gives /books to the uid sys.argv[1].
const addCode = ownDef + `
import subprocess
env = dict(os.environ, CALIBRE_CONFIG_DIRECTORY="/tmp/calibre-config")
add = """
import json
from calibre.library import db
from calibre.ebooks.metadata.book.base import Metadata
lib = db("/books").new_api
books = json.load(open("` + fixturesDir + `/books.json"))
for i in range(0, len(books), 500):
    lib.add_books([(Metadata(b["title"], [b["author"]]), {"EPUB": "` + fixturesDir + `/book.epub"}) for b in books[i:i + 500]])
lib.close()
"""
subprocess.run(["/opt/calibre/calibre-debug", "-c", add], check=True, env=env, stdout=subprocess.DEVNULL)
own("/books", int(sys.argv[1]))
`

// bookLibrary is a new books volume holding a Calibre library of books,
// each a copy of the fixture EPUB, numbered from 1 in their order, made by
// the image's own Calibre and owned by uid 65532.
func bookLibrary(tb testing.TB, e harness.Engine, img string, books []shelf) harness.Mount {
	tb.Helper()
	f := fixtureBooks(tb, e, img)
	list, err := json.Marshal(books)
	if err != nil {
		tb.Fatal(err)
	}
	vol := volume(tb, e, booksDir)
	src := harness.Mount{Source: hostDir(tb, map[string][]byte{"book.epub": f.epub, "books.json": list}), Target: fixturesDir, ReadOnly: true}
	res, err := e.Run(tb.Context(), tb, &harness.RunSpec{
		Image:      img,
		Network:    "none",
		Confine:    asRoot,
		Mounts:     []harness.Mount{vol, src},
		Entrypoint: []string{python},
		Args:       []string{"-I", "-c", addCode, strconv.Itoa(65532)},
	})
	if err != nil || res.Code != 0 {
		tb.Fatalf("making a library of %d books: %v, exit status %d: %s", len(books), err, res.Code, harness.Clip(string(res.Stderr)))
	}
	return vol
}

// titled is n books titled "<prefix> <i>", i from 1, by one author.
func titled(prefix string, n int) []shelf {
	books := make([]shelf, n)
	for i := range books {
		books[i] = shelf{Title: fmt.Sprintf("%s %d", prefix, i+1), Author: epubAuthor}
	}
	return books
}

// comicInfo is the fixture comic's metadata, in ComicInfo.xml, the form
// Calibre-Web reads from a comic archive.
const (
	comicTitle  = "Divbast Fixture Comic"
	comicSeries = "Divbast Series"
	comicWriter = "Divbast Writer"
	comicInfo   = `<?xml version="1.0" encoding="utf-8"?>
<ComicInfo><Title>` + comicTitle + `</Title><Series>` + comicSeries + `</Series><Number>7</Number><Writer>` + comicWriter + `</Writer></ComicInfo>
`
)

// comicFiles are the fixture comic's files: one green page, then its
// metadata.
func comicFiles() []archived {
	return []archived{
		{name: "page01.png", data: solidPNG(300, 400, comicGrn)},
		{name: "ComicInfo.xml", data: []byte(comicInfo)},
	}
}

// archived is one file in an archive.
type archived struct {
	name string
	data []byte
}

// cbz is a comic book as a ZIP archive of files.
func cbz(files []archived) []byte {
	var b bytes.Buffer
	zw := zip.NewWriter(&b)
	for _, f := range files {
		w, err := zw.Create(f.name)
		if err == nil {
			_, err = w.Write(f.data)
		}
		if err != nil {
			panic(err) // an in-memory archive cannot fail
		}
	}
	if err := zw.Close(); err != nil {
		panic(err)
	}
	return b.Bytes()
}

// cbr is a comic book as a RAR archive, version 4, of files, each stored
// as it is: the format WinRAR writes with -m0, laid out by hand, since no
// RAR writer may ship. Each block is a header whose first two bytes are
// the low half of the CRC-32 of the rest of the header.
func cbr(files []archived) []byte {
	var b bytes.Buffer
	b.Write([]byte("Rar!\x1a\x07\x00")) // the marker block
	le := byteorder.LittleEndian
	block := func(header []byte) {
		b.Write(le.AppendUint16(nil, uint16(crc32.ChecksumIEEE(header)))) //nolint:gosec // G115: the format keeps the CRC's low 16 bits
		b.Write(header)
	}
	// The archive header: its type, no flags, its size, and 6 reserved
	// bytes
	main := []byte{0x73, 0, 0}
	main = le.AppendUint16(main, 13)
	block(append(main, make([]byte, 6)...))
	const dosTime = (2026-1980)<<25 | 1<<21 | 1<<16 // 1 January 2026, midnight
	for _, f := range files {
		h := []byte{0x74}
		h = le.AppendUint16(h, 0x8000)                     // a long block: the data follows the header
		h = le.AppendUint16(h, uint16(32+len(f.name)))     //nolint:gosec // G115: the names are short
		h = le.AppendUint32(h, uint32(len(f.data)))        //nolint:gosec // G115: the files are small; packed size
		h = le.AppendUint32(h, uint32(len(f.data)))        //nolint:gosec // G115: unpacked size
		h = append(h, 3)                                   // made on Unix
		h = le.AppendUint32(h, crc32.ChecksumIEEE(f.data)) // the file's CRC-32
		h = le.AppendUint32(h, dosTime)
		h = append(h, 29, 0x30)                     // RAR 2.9 can unpack it; stored
		h = le.AppendUint16(h, uint16(len(f.name))) //nolint:gosec // G115: the names are short
		h = le.AppendUint32(h, 0o100644)            // a regular file, rw-r--r--
		h = append(h, f.name...)
		block(h)
		b.Write(f.data)
	}
	block([]byte{0x7b, 0x00, 0x40, 7, 0}) // the end of the archive
	return b.Bytes()
}

// pdfPage is a PDF of one page, w by h points, filled with c, and then
// pad bytes of an object nothing refers to, read as it is written: a
// large PDF is never held in memory. It returns the PDF and its length.
func pdfPage(w, h int, c rgb, pad int64) (pdf io.Reader, size int64) {
	var head strings.Builder
	head.WriteString("%PDF-1.4\n")
	content := fmt.Sprintf("%.3f %.3f %.3f rg 0 0 %d %d re f\n", float64(c.r)/255, float64(c.g)/255, float64(c.b)/255, w, h)
	objects := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 %d %d] /Contents 4 0 R /Resources << >> >>", w, h),
		fmt.Sprintf("<< /Length %d >>\nstream\n%sendstream", len(content), content),
	}
	offsets := make([]int64, 0, len(objects)+1)
	for i, o := range objects {
		offsets = append(offsets, int64(head.Len()))
		fmt.Fprintf(&head, "%d 0 obj\n%s\nendobj\n", i+1, o)
	}
	padOpen := fmt.Sprintf("%d 0 obj\n<< /Length %d >>\nstream\n", len(objects)+1, pad)
	padClose := "\nendstream\nendobj\n"
	offsets = append(offsets, int64(head.Len()))
	xrefAt := int64(head.Len()) + int64(len(padOpen)) + pad + int64(len(padClose))
	var tail strings.Builder
	fmt.Fprintf(&tail, "xref\n0 %d\n0000000000 65535 f \n", len(offsets)+1)
	for _, off := range offsets {
		fmt.Fprintf(&tail, "%010d 00000 n \n", off)
	}
	fmt.Fprintf(&tail, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(offsets)+1, xrefAt)
	r := io.MultiReader(strings.NewReader(head.String()+padOpen), io.LimitReader(zeros{}, pad), strings.NewReader(padClose+tail.String()))
	return r, int64(head.Len()+len(padOpen)) + pad + int64(len(padClose)+tail.Len())
}

// zeros reads as an endless run of zero bytes.
type zeros struct{}

func (zeros) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}
