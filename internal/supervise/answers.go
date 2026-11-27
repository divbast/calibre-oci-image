// SPDX-License-Identifier: GPL-3.0-only
// SPDX-FileCopyrightText: 2026 Aryan Ameri <github@aryan.ameri.coffee>

package supervise

import (
	"context"
	"net/http"
	"time"
)

// Answers returns a Child.Serves for a child that serves HTTP. It asks
// target with client, and returns once any answer comes, of any status,
// or with ctx's error once ctx ends. Its first try comes every after it
// is called, then one each every, and each try is given every.
//
// The first try waits because a server that acted on a SIGTERM may still
// answer for a moment after it: a request at once could be answered, and
// the supervisor would send a second SIGTERM, which can cut short how
// calibre-server ends. calibre-server answered no request sent after a
// SIGTERM it acted on, but it acts on one in its main thread, which a busy
// machine can hold back. By the first try, one that acted on the SIGTERM
// answers no more, and one that lost it still does.
//
// A connection the server takes and sends nothing on is no answer:
// calibre-server's port takes connections before its serving loop runs,
// and only that loop acts on SIGTERM.
func Answers(client *http.Client, target string, every time.Duration) func(context.Context) error {
	return func(ctx context.Context) error {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, http.NoBody)
		if err != nil {
			return err
		}
		wait := time.NewTimer(every)
		defer wait.Stop()
		for {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-wait.C:
			}
			if answered(ctx, client, req, every) {
				return nil
			}
			wait.Reset(every)
		}
	}
}

// answered sends req with client, and reports whether an answer came
// within within.
func answered(ctx context.Context, client *http.Client, req *http.Request, within time.Duration) bool {
	try, cancel := context.WithTimeout(ctx, within)
	defer cancel()
	resp, err := client.Do(req.Clone(try))
	if err != nil {
		return false
	}
	_ = resp.Body.Close() // only that an answer came matters
	return true
}
