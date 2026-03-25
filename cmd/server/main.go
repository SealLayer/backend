package main

import (
	"context"
	"log/slog"
	"os"
	"strings"

	"github.com/SealLayer/backend/internal/config"
	"github.com/SealLayer/backend/internal/crypto"
	"github.com/SealLayer/backend/internal/httpserver"
	"github.com/SealLayer/backend/internal/logger"
)

func main() {
	cfg, err := config.LoadFromEnv()
	if err != nil {
		slog.Error("failed to load configuration", "err", err)
		os.Exit(1)
	}

	keyMaterial, err := cfg.LoadArmoredPrivateKey()
	if err != nil {
		slog.Error("failed to load GPG private key material", "err", err)
		os.Exit(1)
	}
	if strings.TrimSpace(keyMaterial) != "" {
		if err := crypto.ImportArmoredPrivateKey(context.Background(), keyMaterial, cfg.GpgPassphrase); err != nil {
			slog.Error("gpg private key import failed", "err", err)
			os.Exit(1)
		}
		if gh := strings.TrimSpace(os.Getenv("GNUPGHOME")); gh != "" {
			slog.Info("gpg private key imported into keyring", "GNUPGHOME", gh)
		} else {
			slog.Info("gpg private key imported into keyring")
		}
	}

	lvl := logger.LevelFromEnv()
	log := logger.NewStdout(lvl)
	startAttrs := []any{
		logger.Op, "startup",
		"log_level", lvl.String(),
		"http_listen", cfg.HTTPListenAddr,
		"github_owner", cfg.GitHubOwner,
		"github_repo", cfg.GitHubRepo,
		"github_branch", cfg.GitHubBranch,
		"git_author_name", cfg.GitAuthorName,
		"git_author_email", cfg.GitAuthorEmail,
		"queue_capacity", cfg.QueueCapacity,
		"batch_interval", cfg.BatchInterval.String(),
		"request_pending_max", cfg.RequestPendingMax.String(),
		"git_sign_commits", cfg.GitSignCommits,
	}
	if cfg.GpgProgram != "" {
		startAttrs = append(startAttrs, "gpg_program", cfg.GpgProgram)
	}
	log.Info("starting application", startAttrs...)

	srv := httpserver.NewServer(cfg, log)

	if err := srv.Run(); err != nil {
		log.Error("server exited", logger.Op, "server_exit", "err", err)
		os.Exit(1)
	}
}
