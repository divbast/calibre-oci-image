// SPDX-License-Identifier: GPL-3.0-only
// SPDX-FileCopyrightText: 2026 Aryan Ameri <github@aryan.ameri.coffee>

package acceptance

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/divbast/ci/contract"
	"github.com/divbast/ci/harness"
)

// What the image's contract says of every container here.
const (
	// tmpfsSize is the size of /tmp the image is run with. Every container
	// gets exactly this, so no check passes on more room than the
	// contract grants.
	tmpfsSize = "2g"
	// appPort serves Calibre's Content Server, and webPort Calibre-Web,
	// both through PID 1.
	appPort = 8080
	webPort = 8083
	// serverLoopback and webLoopback are where the two apps themselves
	// listen, on 127.0.0.1 alone.
	serverLoopback = 18080
	webLoopback    = 18083
)

// binary is the image's command: PID 1, the health check, and the
// contract's Exec.
const binary = "/usr/local/bin/divbast-calibre"

// serverArgv is PID 1's argv, which stays PID 1 and starts both apps.
var serverArgv = []string{binary, "serve"}

// healthCmd is the shipped health probe, as the image's HEALTHCHECK runs
// it and a Podman user passes it with --health-cmd.
var healthCmd = []string{binary, "health"}

// TestContract runs divbast's confinement contract against the image, on a
// network with no route out: no capability, no new privileges, a seccomp
// filter, a read-only root and no writable mount that can execute but the
// two volumes, uid 65532, PID 1 the image's command, no shell and no
// package manager, the four TCP ports and no UDP, no egress, and the
// HEALTHCHECK. It starts on empty /books and /config volumes, with both
// admin passwords as files, so its first start runs under the contract
// too, and its checks begin once the shipped probe passes. The Content
// Server's own page answers 200 on 8080 with no login.
func TestContract(t *testing.T) {
	t.Parallel()
	e, image := underTest(t)
	tb := redacting(t)
	sec := newSecrets(tb)
	var ran *harness.Container // the container the contract checks
	var internal string        // the internal user's password in it
	contract.Run(t, &contract.Config{
		Image:         image,
		AppPort:       appPort,
		HealthPort:    0,
		HealthPath:    "/",
		TmpfsSize:     tmpfsSize,
		PID1:          serverArgv,
		Exec:          []string{binary, "version"},
		ExtraTCPPorts: []int{webPort, serverLoopback, webLoopback},
		HealthCmd:     healthCmd,
		Redact:        tb.redact,
		Healthcheck: contract.Healthcheck{
			Test:        slices.Concat([]string{"CMD"}, healthCmd),
			Interval:    30 * time.Second,
			Timeout:     10 * time.Second,
			StartPeriod: 60 * time.Second,
			Retries:     3,
		},
		Env: sec.env(),
		Mounts: func(tb testing.TB) []harness.Mount {
			tb.Helper()
			return append(newStore(tb, e).mounts(), sec.mount())
		},
		Ready: func(ctx context.Context, c *harness.Container) error {
			ran = c
			if err := waitHealthy(ctx, c); err != nil {
				return err
			}
			pw, err := internalPassword(ctx, c)
			if err != nil {
				return fmt.Errorf("reading the internal user's password: %w", err)
			}
			tb.hide(pw)
			internal = pw
			return nil
		},
	})
	// Its container's log must hold none of the three passwords, as every
	// other container's does
	if ran == nil {
		tb.Fatal("the contract never asked whether its container was ready")
	}
	checkLog(tb.Context(), tb, ran, append(sec.all(), internal))
}
