package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"time"

	"github.com/SealLayer/backend/internal/config"
	"github.com/SealLayer/backend/internal/crypto"
	"github.com/SealLayer/backend/internal/githubclient"
	"github.com/SealLayer/backend/internal/gitops"
	"github.com/SealLayer/backend/internal/ledger"
	"github.com/SealLayer/backend/internal/logger"
	"github.com/SealLayer/backend/internal/queue"
	"github.com/google/go-github/v66/github"
)

type Worker struct {
	cfg    config.Config
	log    *slog.Logger
	q      *queue.Queue
	store  *queue.BatchStore
	gh     *githubclient.Client
	closed chan struct{}
}

const (
	chainLookbackDays = 3660
	chainGenesisSeed  = "SIP-v1-global-genesis"
	reasonQueued      = "queued"
	reasonBatching    = "batching"
	reasonPushed      = "pushed"
	reasonInvalid     = "invalid_batch"
	reasonGitHubFetch = "github_fetch_failed"
	reasonLedgerParse = "ledger_parse_failed"
	reasonAppend      = "ledger_append_failed"
	reasonGpgSign     = "gpg_sign_failed"
	reasonGitPush     = "git_push_failed"
	reasonInternal    = "internal_error"
)

func New(cfg config.Config, log *slog.Logger, q *queue.Queue, store *queue.BatchStore) *Worker {
	return &Worker{
		cfg:    cfg,
		log:    log,
		q:      q,
		store:  store,
		gh:     githubclient.New(cfg.GitHubToken),
		closed: make(chan struct{}),
	}
}

func (w *Worker) Start(ctx context.Context) {
	t := time.NewTicker(w.cfg.BatchInterval)
	defer t.Stop()
	defer close(w.closed)

	for {
		select {
		case <-ctx.Done():
			w.log.Info("worker stopping",
				logger.Op, "worker_stop",
				"reason", ctx.Err(),
			)
			return
		case <-t.C:
			w.processOnce(ctx)
		}
	}
}

func (w *Worker) WaitClosed() { <-w.closed }

func (w *Worker) processOnce(ctx context.Context) {
	jobs := w.q.Drain(w.cfg.QueueCapacity)
	if len(jobs) == 0 {
		w.log.Debug("batch: queue empty, nothing to do",
			logger.Op, "batch_tick",
		)
		return
	}

	// batch_id is fixed at enqueue time; must not be recomputed on the worker tick or status keys drift.
	for _, group := range groupJobsByBatchID(jobs) {
		w.processBatchGroup(ctx, group)
	}
}

// processBatchGroup processes all jobs sharing one batch_id in a single ledger write.
func (w *Worker) processBatchGroup(ctx context.Context, jobs []*queue.SealJob) {
	if len(jobs) == 0 {
		return
	}
	batchID := jobs[0].BatchID
	for _, j := range jobs[1:] {
		if j.BatchID != batchID {
			w.log.Error("batch: inconsistent job batch_id (unexpected)",
				logger.Op, "batch_invariant",
				logger.BatchID, batchID,
				"got", j.BatchID,
			)
			w.failWithReason(batchID, "", reasonInvalid, false, fmt.Errorf("internal batch_id mismatch"), jobs)
			return
		}
	}

	now := time.Now().UTC()

	w.log.Info("batch: processing started",
		logger.Op, "batch_start",
		logger.BatchID, batchID,
		"job_count", len(jobs),
	)

	w.store.Put(queue.BatchState{
		BatchID:    batchID,
		Status:     queue.StatusBatching,
		ReasonCode: reasonBatching,
		Retryable:  true,
		UpdatedAt:  time.Now().UTC(),
	})

	ledgerPath := ledgerPathForTime(now)
	w.log.Info("ledger: target file",
		logger.Op, "ledger_target",
		logger.BatchID, batchID,
		"ledger_path", ledgerPath,
		"branch", w.cfg.GitHubBranch,
	)

	var lastContent string
	var err error

	// retry loop for conflict-like failures
	for attempt := 1; attempt <= 3; attempt++ {
		w.log.Info("github: fetching ledger file",
			logger.Op, "github_get_file",
			logger.BatchID, batchID,
			"attempt", attempt,
			"ledger_path", ledgerPath,
		)
		lastContent, _, _, err = w.gh.GetFile(ctx, w.cfg.GitHubOwner, w.cfg.GitHubRepo, ledgerPath, w.cfg.GitHubBranch)
		if err != nil {
			w.failWithReason(batchID, ledgerPath, reasonGitHubFetch, true, fmt.Errorf("failed to fetch file: %w", err), jobs)
			return
		}

		prevHash, err := w.resolvePrevHash(ctx, now, lastContent)
		if err != nil {
			w.failWithReason(batchID, ledgerPath, reasonLedgerParse, false, fmt.Errorf("failed to parse last final hash: %w", err), jobs)
			return
		}

		w.log.Info("ledger: previous final hash loaded",
			logger.Op, "ledger_prev_hash",
			logger.BatchID, batchID,
			"prev_hash_prefix", prefixHex(prevHash, 16),
		)

		rows := make([]ledger.Row, 0, len(jobs))
		finals := make([]string, 0, len(jobs))
		ts := time.Now().UTC().Unix()
		for i, j := range jobs {
			final := crypto.FinalHash(j.ContentHash, prevHash, ts)
			r := ledger.Row{
				ID:          queue.RowID(batchID, i),
				ContentHash: j.ContentHash,
				PrevHash:    prevHash,
				TS:          ts,
				FinalHash:   final,
			}
			rows = append(rows, r)
			finals = append(finals, final)
			prevHash = final
		}

		w.log.Info("ledger: rows built",
			logger.Op, "ledger_rows",
			logger.BatchID, batchID,
			"row_count", len(rows),
			"ts_utc", ts,
		)

		updated, err := ledger.AppendRows(lastContent, rows)
		if err != nil {
			w.failWithReason(batchID, ledgerPath, reasonAppend, false, fmt.Errorf("append failed: %w", err), jobs)
			return
		}

		batchRoot := crypto.BatchRoot(finals)
		w.log.Info("crypto: batch root computed, requesting detached signature",
			logger.Op, "gpg_sign_batch_root",
			logger.BatchID, batchID,
			"batch_root_prefix", prefixHex(batchRoot, 16),
		)
		sigB64, sigErr := crypto.SignDetachedArmorBase64(ctx, w.cfg.GpgKeyID, w.cfg.GpgPassphrase, []byte(batchRoot))
		if sigErr != nil {
			w.failWithReason(batchID, ledgerPath, reasonGpgSign, false, fmt.Errorf("detached signature failed: %w", sigErr), jobs)
			return
		}
		w.log.Info("crypto: detached signature produced",
			logger.Op, "gpg_sign_batch_root",
			logger.BatchID, batchID,
			"sig_b64_len", len(sigB64),
		)

		commitMsg := fmt.Sprintf("seal batch %s", batchID)

		commitSHA, pushErr := gitops.SignedCloneCommitPush(ctx, gitops.SignedPushOptions{
			RepoURL:        w.cfg.GitHubRepoURL,
			Owner:          w.cfg.GitHubOwner,
			Repo:           w.cfg.GitHubRepo,
			Branch:         w.cfg.GitHubBranch,
			GitHubToken:    w.cfg.GitHubToken,
			GpgKeyID:       w.cfg.GpgKeyID,
			GpgPassphrase:  w.cfg.GpgPassphrase,
			GpgProgram:     w.cfg.GpgProgram,
			GitSignCommits: w.cfg.GitSignCommits,
			AuthorName:     w.cfg.GitAuthorName,
			AuthorEmail:    w.cfg.GitAuthorEmail,
			Log:            w.log,
		}, ledgerPath, updated, commitMsg)
		if pushErr == nil && commitSHA != "" {
			w.log.Info("git: push succeeded, batch complete",
				logger.Op, "batch_complete",
				logger.BatchID, batchID,
				"commit_sha", commitSHA,
				"ledger_path", ledgerPath,
			)
			w.store.Put(queue.BatchState{
				BatchID:    batchID,
				Status:     queue.StatusPushed,
				LedgerPath: ledgerPath,
				CommitSHA:  commitSHA,
				ReasonCode: reasonPushed,
				Retryable:  false,
				UpdatedAt:  time.Now().UTC(),
			})
			for _, j := range jobs {
				receipt := map[string]any{
					"version":                "SIP-v1",
					"seal_id":                fmt.Sprintf("%s-%d", batchID, rand.Intn(1_000_000)),
					"public_key_fingerprint": w.cfg.GpgPublicKeyFingerprint,
					"hashes": map[string]any{
						"content": j.ContentHash,
						"final":   "", // filled below
					},
					"ledger": map[string]any{
						"path":       ledgerPath,
						"commit_sha": commitSHA,
						"batch_id":   batchID,
					},
					"ts_utc": ts,
					"signature": map[string]any{
						"alg": "openpgp-detached",
						"sig": sigB64,
					},
				}
				// Find this job's final hash (by row index)
				for idx, row := range rows {
					if row.ContentHash == j.ContentHash && row.ID == queue.RowID(batchID, idx) {
						receipt["hashes"].(map[string]any)["final"] = row.FinalHash
						break
					}
				}
				j.ResultCh <- queue.SealJobResult{BatchID: batchID, LedgerPath: ledgerPath, CommitSHA: commitSHA, Receipt: receipt}
			}
			return
		}

		if ghErr, ok := pushErr.(*github.ErrorResponse); ok && ghErr.Response != nil && ghErr.Response.StatusCode == 409 {
			w.log.Warn("github: 409 conflict, retrying",
				logger.Op, "git_push_retry",
				logger.BatchID, batchID,
				"attempt", attempt,
			)
			continue
		}

		w.log.Error("git: push failed",
			logger.Op, "git_push_error",
			logger.BatchID, batchID,
			"attempt", attempt,
			"err", pushErr,
		)
		time.Sleep(time.Duration(attempt) * 300 * time.Millisecond)
	}

	w.failWithReason(batchID, ledgerPath, reasonGitPush, true, fmt.Errorf("push max retries exceeded"), jobs)
}

// groupJobsByBatchID groups by batch_id while preserving drain order (multiple batches per tick possible).
func groupJobsByBatchID(jobs []*queue.SealJob) [][]*queue.SealJob {
	seen := make(map[string]int)
	var out [][]*queue.SealJob
	for _, j := range jobs {
		id := j.BatchID
		if i, ok := seen[id]; ok {
			out[i] = append(out[i], j)
			continue
		}
		seen[id] = len(out)
		out = append(out, []*queue.SealJob{j})
	}
	return out
}

func ledgerPathForTime(t time.Time) string {
	return fmt.Sprintf("ledger/%04d/%02d/%02d.jsonl", t.Year(), int(t.Month()), t.Day())
}

// resolvePrevHash returns the chain anchor for the next row to be written today.
// Order:
// 1) last final_hash inside today's ledger file (if present),
// 2) nearest previous day containing a final_hash (global chain continuity),
// 3) deterministic genesis for the very first ledger write.
func (w *Worker) resolvePrevHash(ctx context.Context, today time.Time, todayContent string) (string, error) {
	prevHash, err := ledger.ParseLastFinalHash(todayContent)
	if err == nil {
		return prevHash, nil
	}
	if !errors.Is(err, ledger.ErrNoFinalHash) {
		return "", err
	}

	for day := 1; day <= chainLookbackDays; day++ {
		candidateDay := today.AddDate(0, 0, -day)
		candidatePath := ledgerPathForTime(candidateDay)
		content, _, exists, getErr := w.gh.GetFile(ctx, w.cfg.GitHubOwner, w.cfg.GitHubRepo, candidatePath, w.cfg.GitHubBranch)
		if getErr != nil {
			return "", fmt.Errorf("fetch previous ledger %s: %w", candidatePath, getErr)
		}
		if !exists {
			continue
		}
		prevHash, parseErr := ledger.ParseLastFinalHash(content)
		if parseErr == nil {
			w.log.Info("ledger: previous day final hash loaded",
				logger.Op, "ledger_prev_hash_backfill",
				"source_ledger_path", candidatePath,
				"days_back", day,
				"prev_hash_prefix", prefixHex(prevHash, 16),
			)
			return prevHash, nil
		}
		if errors.Is(parseErr, ledger.ErrNoFinalHash) {
			continue
		}
		return "", fmt.Errorf("parse previous ledger %s: %w", candidatePath, parseErr)
	}

	genesis := crypto.Sha256HexLower(chainGenesisSeed)
	w.log.Warn("ledger: no prior final hash found, using deterministic genesis anchor",
		logger.Op, "ledger_prev_hash_genesis",
		"lookback_days", chainLookbackDays,
		"genesis_seed", chainGenesisSeed,
		"prev_hash_prefix", prefixHex(genesis, 16),
	)
	return genesis, nil
}

// prefixHex returns a short hex prefix for logs (full hash is not logged).
func prefixHex(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func (w *Worker) failWithReason(batchID, ledgerPath, reasonCode string, retryable bool, err error, jobs []*queue.SealJob) {
	w.log.Error("batch: failed",
		logger.Op, "batch_fail",
		logger.BatchID, batchID,
		"ledger_path", ledgerPath,
		"reason_code", reasonCode,
		"retryable", retryable,
		"err", err,
		"affected_jobs", len(jobs),
	)
	w.store.Put(queue.BatchState{
		BatchID:    batchID,
		Status:     queue.StatusFailed,
		LedgerPath: ledgerPath,
		Error:      err.Error(),
		ReasonCode: reasonCode,
		Retryable:  retryable,
		UpdatedAt:  time.Now().UTC(),
	})
	for _, j := range jobs {
		j.ResultCh <- queue.SealJobResult{BatchID: batchID, LedgerPath: ledgerPath, Err: err}
	}
}
