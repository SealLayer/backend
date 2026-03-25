package gitops

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/SealLayer/backend/internal/logger"
)

type SignedPushOptions struct {
	RepoURL       string
	Owner         string
	Repo          string
	Branch        string
	GitHubToken   string
	GpgKeyID      string
	GpgPassphrase string
	// GpgProgram: if set, runs "git config gpg.program" after clone (when Git and gpg differ).
	GpgProgram string
	// GitSignCommits: if false, unsigned Git commits; detached receipt signature is still produced elsewhere.
	GitSignCommits bool
	AuthorName     string
	AuthorEmail    string
	// Log is optional; if nil, git step logs are skipped (caller may log separately).
	Log *slog.Logger
}

func SignedCloneCommitPush(ctx context.Context, opts SignedPushOptions, relativePath string, newContent string, commitMessage string) (commitSHA string, err error) {
	log := opts.Log
	logGit := func(msg string, op string, args ...any) {
		if log == nil {
			return
		}
		a := []any{logger.Op, op}
		a = append(a, args...)
		log.Info(msg, a...)
	}

	tmpDir, err := os.MkdirTemp("", "seallayer-ledger-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmpDir)

	repoDir := filepath.Join(tmpDir, "repo")

	pushURL := tokenizedGitURL(opts.Owner, opts.Repo, opts.GitHubToken)

	logGit("git: clone starting", "git_clone",
		"branch", opts.Branch,
		"relative_path", relativePath,
		"temp_dir", tmpDir,
	)
	if err := run(ctx, tmpDir, nil, "git", "clone", "--depth", "1", "--branch", opts.Branch, pushURL, repoDir); err != nil {
		return "", fmt.Errorf("git clone: %w", err)
	}
	logGit("git: clone done", "git_clone")

	fullPath := filepath.Join(repoDir, filepath.FromSlash(relativePath))
	if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
		return "", fmt.Errorf("mkdir: %w", err)
	}
	logGit("git: writing ledger file", "git_write_ledger",
		"path", relativePath,
		"bytes", len(newContent),
	)
	if err := os.WriteFile(fullPath, []byte(newContent), 0644); err != nil {
		return "", fmt.Errorf("write: %w", err)
	}

	env := []string{
		"GIT_AUTHOR_NAME=" + opts.AuthorName,
		"GIT_AUTHOR_EMAIL=" + opts.AuthorEmail,
		"GIT_COMMITTER_NAME=" + opts.AuthorName,
		"GIT_COMMITTER_EMAIL=" + opts.AuthorEmail,
	}

	if p := strings.TrimSpace(opts.GpgProgram); p != "" {
		if err := run(ctx, repoDir, nil, "git", "config", "gpg.program", p); err != nil {
			return "", fmt.Errorf("git config gpg.program: %w", err)
		}
		logGit("git: gpg.program set in local repo", "git_gpg_program", "path", p)
	}

	// Signing: no global git config; only per-invocation -c flags.
	var gitBase []string
	if opts.GitSignCommits {
		gitBase = []string{
			"-c", "commit.gpgsign=true",
			"-c", "user.signingkey=" + opts.GpgKeyID,
		}
	} else {
		gitBase = []string{"-c", "commit.gpgsign=false"}
	}

	logGit("git: add", "git_add", "path", relativePath)
	if err := run(ctx, repoDir, env, "git", append(gitBase, "add", "--", relativePath)...); err != nil {
		return "", fmt.Errorf("git add: %w", err)
	}

	if opts.GitSignCommits {
		gpgEnv := append([]string{}, env...)
		gpgEnv = append(gpgEnv, "GIT_TRACE=0")
		commitArgs := append(gitBase,
			"commit",
			"-m", commitMessage,
			"-S"+opts.GpgKeyID,
			"--gpg-sign="+opts.GpgKeyID,
		)
		if tty := os.Getenv("GPG_TTY"); tty != "" {
			gpgEnv = append(gpgEnv, "GPG_TTY="+tty)
		}
		logGit("git: signed commit", "git_commit_signed",
			"message", commitMessage,
			"signing_key", opts.GpgKeyID,
		)
		if err := runWithPassphrase(ctx, repoDir, gpgEnv, opts.GpgPassphrase, "git", commitArgs...); err != nil {
			return "", fmt.Errorf("git commit (signed): %w", err)
		}
		logGit("git: commit done", "git_commit_signed")
	} else {
		commitArgs := append(gitBase, "commit", "-m", commitMessage)
		logGit("git: unsigned commit", "git_commit_unsigned", "message", commitMessage)
		if err := run(ctx, repoDir, env, "git", commitArgs...); err != nil {
			return "", fmt.Errorf("git commit: %w", err)
		}
		logGit("git: commit done", "git_commit_unsigned")
	}

	logGit("git: push origin", "git_push", "branch", opts.Branch)
	if err := run(ctx, repoDir, env, "git", "push", "origin", opts.Branch); err != nil {
		return "", fmt.Errorf("git push: %w", err)
	}
	logGit("git: push done", "git_push")

	out, err := output(ctx, repoDir, env, "git", "rev-parse", "HEAD")
	if err != nil {
		return "", fmt.Errorf("rev-parse: %w", err)
	}
	sha := strings.TrimSpace(out)
	logGit("git: HEAD rev-parse", "git_rev_parse", "commit_sha", sha)
	return sha, nil
}

func tokenizedGitURL(owner, repo, token string) string {
	// x-access-token works for GitHub HTTPS auth with tokens.
	return fmt.Sprintf("https://x-access-token:%s@github.com/%s/%s.git", token, owner, repo)
}

func run(ctx context.Context, dir string, env []string, name string, args ...string) error {
	cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cctx, name, args...)
	cmd.Dir = dir
	if env != nil {
		cmd.Env = append(os.Environ(), env...)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %w (%s)", name, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func runWithPassphrase(ctx context.Context, dir string, env []string, passphrase string, name string, args ...string) error {
	// Configure gpg to use loopback. We rely on gpg's own pinentry-mode loopback.
	// Setting this via env is not standardized, so we pass it through git config elsewhere.
	cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cctx, name, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	// Provide passphrase to gpg via stdin if it asks (best-effort).
	cmd.Stdin = strings.NewReader(passphrase + "\n")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %w (%s)", name, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func output(ctx context.Context, dir string, env []string, name string, args ...string) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cctx, name, args...)
	cmd.Dir = dir
	if env != nil {
		cmd.Env = append(os.Environ(), env...)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%s: %w (%s)", name, err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}
