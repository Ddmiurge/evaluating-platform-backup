// Command embeddedbuild is the deploy-host half of the embedded-plugin build
// pipeline (P28 / design §9 E-4, D-10: the lightweight button only writes a
// marker — THIS tool consumes it; it refuses to run with no pending work).
//
// It runs on the machine that holds the master key (the same trust domain as
// the backend: .env + Docker + DB are all host-local, per the 2026-11-09
// discussion — the red line is that neither the key nor plaintext ever leaves
// this host).
//
// Two modes, so the shell orchestrator can place a deploy between them:
//
//	go run ./cmd/embeddedbuild -stage
//	  plan the batch (union of build_requested ∪ testing ∪ approved ∪
//	  disabled, minus removals — see batch.go) → decrypt → re-validate →
//	  stage promptfoo_engine/embedded-plugins/<id>/ + index.json (with
//	  removed_ids) → rewrite testdata/embedded_plugins.json (wizard catalog
//	  source) → print the batch id on stdout. Rows keep their states: a crash
//	  here is retryable, nothing is claimed as baked.
//	  Prints NO_QUEUE (exit 0) when nothing is staged and nothing is marked
//	  for removal — the orchestrator then skips deploy AND finalize; any
//	  stale index.json is deleted so "index exists ⟺ batch awaits finalize".
//
//	go run ./cmd/embeddedbuild -finalize -ok                 (after deploy succeeded)
//	go run ./cmd/embeddedbuild -finalize -fail -reason=…     (any step failed)
//	  consume THIS batch's index.json (never the live queue): batch packages
//	  build_requested → testing | build_failed, removed_ids → removed
//	  (removals process even when the batch has no packages). A row that
//	  moved since stage is skipped with a warning (skip-on-conflict; the
//	  deploy already happened, so aborting would strand the rest). On success
//	  the index is cleared; a failed run keeps it so the orchestrator can
//	  retry. Exactly one of -ok / -fail is required; -fail requires -reason.
//	  All flag validation happens before any DB connection, so a rejected
//	  invocation provably touches no rows.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	appcrypto "evaluating_platform/internal/crypto"
	maclawpkg "evaluating_platform/internal/maclaw"
	"evaluating_platform/internal/repository"
	"evaluating_platform/pkg/config"
	"evaluating_platform/pkg/db"
)

// fatal prints a one-line error (the shell orchestrator treats any exit as a
// failed batch; the state machine keeps rows in build_requested for retry).
func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "embeddedbuild: "+format+"\n", args...)
	os.Exit(1)
}

// flagSet groups the parsed flags; registerFlags exists so tests can assert
// the documented invocations are actually definable (review finding 4: the
// file header used to promise a -fail flag that did not exist).
type flagSet struct {
	config   *string
	root     *string
	stage    *bool
	finalize *bool
	ok       *bool
	fail     *bool
	reason   *string
}

func registerFlags(fs *flag.FlagSet) *flagSet {
	return &flagSet{
		config:   fs.String("config", "configs/config.yaml", "config file (same as the server)"),
		root:     fs.String("root", ".", "repository root (staging + testdata live under it)"),
		stage:    fs.Bool("stage", false, "plan the batch, stage the tree, rewrite the catalog file"),
		finalize: fs.Bool("finalize", false, "transition this batch's rows after deploy (requires -ok or -fail)"),
		ok:       fs.Bool("ok", false, "with -finalize: the deploy succeeded (build_requested → testing)"),
		fail:     fs.Bool("fail", false, "with -finalize: the deploy failed (build_requested → build_failed; requires -reason)"),
		reason:   fs.String("reason", "", "with -finalize -fail: the failure summary (safe text only)"),
	}
}

// finalizeIntent is the resolved meaning of a -finalize invocation.
type finalizeIntent struct {
	Ok     bool
	Reason string
}

const finalizeUsage = "use -finalize -ok (deploy succeeded) or -finalize -fail -reason=… (deploy failed); exactly one of -ok/-fail is required"

// parseFinalizeIntent validates the -finalize flag combination. An error here
// must be raised BEFORE any DB connection — a rejected invocation touches no
// rows (review finding 4: bare -finalize used to silently mean "fail the
// whole batch").
func parseFinalizeIntent(ok, fail bool, reason string) (finalizeIntent, error) {
	if ok && fail {
		return finalizeIntent{}, fmt.Errorf("-ok and -fail are mutually exclusive; %s", finalizeUsage)
	}
	if !ok && !fail {
		return finalizeIntent{}, fmt.Errorf("missing deploy outcome; %s", finalizeUsage)
	}
	if fail && strings.TrimSpace(reason) == "" {
		return finalizeIntent{}, fmt.Errorf("-fail requires -reason (a failed batch must be explained); %s", finalizeUsage)
	}
	if ok {
		return finalizeIntent{Ok: true}, nil
	}
	return finalizeIntent{Ok: false, Reason: strings.TrimSpace(reason)}, nil
}

func main() {
	flags := registerFlags(flag.CommandLine)
	flag.Parse()

	if *flags.stage == *flags.finalize {
		fatal("exactly one of -stage or -finalize is required")
	}

	// Validate the finalize intent before touching config/DB: a rejected
	// command must provably change no rows.
	var intent finalizeIntent
	if *flags.finalize {
		var err error
		intent, err = parseFinalizeIntent(*flags.ok, *flags.fail, *flags.reason)
		if err != nil {
			fatal("%v", err)
		}
	}

	cfg, err := config.Load(*flags.config)
	if err != nil {
		fatal("load config: %v", err)
	}
	ctx := context.Background()
	pool, err := db.NewPostgresPool(ctx, cfg.Database.DSN, cfg.Database.MaxConns, cfg.Database.MinConns)
	if err != nil {
		fatal("connect postgres: %v", err)
	}
	defer pool.Close()

	embeddedRepo := repository.NewMaclawEmbeddedPluginRepository(pool)
	lifecycle := maclawpkg.NewEmbeddedPluginLifecycle(embeddedRepo)

	if *flags.stage {
		keyStore, err := appcrypto.NewKeyStore(cfg.Crypto)
		if err != nil {
			fatal("keystore: %v", err)
		}
		resourceRepo := repository.NewMaclawResourceRecordRepository(pool)
		if err := runStage(ctx, embeddedRepo, resourceRepo, keyStore, *flags.root); err != nil {
			fatal("stage: %v", err)
		}
		return
	}
	if err := runFinalize(ctx, embeddedRepo, lifecycle, *flags.root, intent); err != nil {
		fatal("finalize: %v", err)
	}
}
