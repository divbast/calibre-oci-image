// SPDX-License-Identifier: GPL-3.0-only
// SPDX-FileCopyrightText: 2026 Aryan Ameri <github@aryan.ameri.coffee>

package pins

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// served is what the test server serves at /file, and its sha256.
const served = "the bytes of a release"

func servedSum() string {
	h := sha256.Sum256([]byte(served))
	return hex.EncodeToString(h[:])
}

func TestFillHashes(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/file" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(served))
	}))
	t.Cleanup(srv.Close)

	const other = "0000000000000000000000000000000000000000000000000000000000000000"
	head := "ARG VERSION=v1\nARG FILE_SHA256="
	fills := []Fill{{Arg: "FILE_SHA256", URL: srv.URL + "/${VERSION}/file"}}
	for _, tt := range []struct {
		name, cf, want, wantErr string
		wantNotes               int
	}{
		{"empty-is-filled", head + "\nFROM x\n", head + servedSum() + "\nFROM x\n", "", 1},
		{"matching-is-unchanged", head + servedSum() + "\n", head + servedSum() + "\n", "", 0},
		{"mismatch-fails", head + other + "\n", "", "FILE_SHA256", 0},
		{"missing-arg-fails", "ARG VERSION=v1\n", "", "FILE_SHA256", 0},
		{"not-found-fails", head + "\n", "", srv.URL + "/v2/file", 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := fills
			if tt.name == "not-found-fails" {
				f = []Fill{{Arg: "FILE_SHA256", URL: srv.URL + "/v2/file"}}
			}
			out, notes, err := FillHashes(t.Context(), srv.Client(), []byte(tt.cf), f)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("FillHashes() = %q, %v; want an error naming %q", out, err, tt.wantErr)
				}
				if tt.name == "not-found-fails" && !strings.Contains(err.Error(), "404") {
					t.Errorf("error %q does not say 404", err)
				}
				return
			}
			if err != nil || string(out) != tt.want || len(notes) != tt.wantNotes {
				t.Errorf("FillHashes() = %q, %q, %v; want %q and %d notes", out, notes, err, tt.want, tt.wantNotes)
			}
		})
	}
}
