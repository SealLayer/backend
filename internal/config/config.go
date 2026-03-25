package config

import (
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	HTTPListenAddr string

	GitHubToken   string
	GitHubOwner   string
	GitHubRepo    string
	GitHubBranch  string
	GitHubRepoURL string

	QueueCapacity        int
	BatchInterval        time.Duration
	RequestPendingMax    time.Duration
	SealPerIpPerMinute   int
	LedgerRepoBaseRawURL string

	GpgKeyID                string
	GpgPassphrase           string
	// GpgPrivateKey is raw armored secret key text (e.g. from CI secret). Prefer file in production.
	GpgPrivateKey string
	// GpgPrivateKeyFile is a path to private.asc (e.g. Docker secret mount /run/secrets/gpg_private.asc).
	GpgPrivateKeyFile string
	// GpgPrivateKeyArmoredB64 is base64(UTF-8 armored key) — use when the host mangles multiline secrets.
	GpgPrivateKeyArmoredB64 string
	GpgPublicKeyFingerprint string

	GitAuthorName  string
	GitAuthorEmail string

	// GpgProgram, if set, is applied as local repo "gpg.program" (needed when Git uses a different gpg than the keyring).
	GpgProgram string
	// GitSignCommits: if false, Git commits are not OpenPGP-signed; detached receipt signature is still produced.
	GitSignCommits bool
}

func LoadFromEnv() (Config, error) {
	cfg := Config{
		HTTPListenAddr:          envString("HTTP_LISTEN_ADDR", ":8080"),
		GitHubOwner:             envString("GITHUB_OWNER", ""),
		GitHubRepo:              envString("GITHUB_REPO", ""),
		GitHubBranch:            envString("GITHUB_BRANCH", "main"),
		GitHubToken:             envString("GITHUB_TOKEN", ""),
		GitHubRepoURL:           envString("GITHUB_REPO_URL", ""),
		QueueCapacity:           envInt("QUEUE_CAPACITY", 1000),
		BatchInterval:           envDuration("BATCH_INTERVAL", 10*time.Second),
		RequestPendingMax:       envDuration("REQUEST_PENDING_MAX", 30*time.Second),
		SealPerIpPerMinute:      envInt("SEAL_PER_IP_PER_MINUTE", 2),
		LedgerRepoBaseRawURL:    envString("LEDGER_RAW_BASE_URL", ""),
		GpgKeyID:                envString("GPG_KEYID", ""),
		GpgPassphrase:           envString("GPG_PASSPHRASE", ""),
		GpgPrivateKey:           envString("GPG_PRIVATE_KEY_ARMORED", ""),
		GpgPrivateKeyArmoredB64: envString("GPG_PRIVATE_KEY_ARMORED_B64", ""),
		GpgPrivateKeyFile:       envString("GPG_PRIVATE_KEY_FILE", ""),
		GpgPublicKeyFingerprint: envString("GPG_PUBLIC_KEY_FINGERPRINT", ""),
		GitAuthorName:           envString("GIT_AUTHOR_NAME", ""),
		GitAuthorEmail:          envString("GIT_AUTHOR_EMAIL", ""),
		GpgProgram:              envString("GPG_PROGRAM", ""),
		GitSignCommits:          envBool("GIT_SIGN_COMMITS", true),
	}

	if cfg.LedgerRepoBaseRawURL == "" && cfg.GitHubOwner != "" && cfg.GitHubRepo != "" {
		cfg.LedgerRepoBaseRawURL = fmt.Sprintf("https://raw.githubusercontent.com/%s/%s", cfg.GitHubOwner, cfg.GitHubRepo)
	}
	if cfg.GitHubRepoURL == "" && cfg.GitHubOwner != "" && cfg.GitHubRepo != "" {
		cfg.GitHubRepoURL = fmt.Sprintf("https://github.com/%s/%s.git", cfg.GitHubOwner, cfg.GitHubRepo)
	}

	var missing []string
	if cfg.GitHubOwner == "" {
		missing = append(missing, "GITHUB_OWNER")
	}
	if cfg.GitHubRepo == "" {
		missing = append(missing, "GITHUB_REPO")
	}
	if cfg.GitHubToken == "" {
		missing = append(missing, "GITHUB_TOKEN")
	}
	if cfg.GitAuthorName == "" {
		missing = append(missing, "GIT_AUTHOR_NAME")
	}
	if cfg.GitAuthorEmail == "" {
		missing = append(missing, "GIT_AUTHOR_EMAIL")
	}
	if len(missing) > 0 {
		return Config{}, errors.New("missing required environment variables: " + strings.Join(missing, ", "))
	}

	return cfg, nil
}

// LoadArmoredPrivateKey resolves key material: file path > base64 env > raw armored env.
func (c Config) LoadArmoredPrivateKey() (string, error) {
	if strings.TrimSpace(c.GpgPrivateKeyFile) != "" {
		b, err := os.ReadFile(c.GpgPrivateKeyFile)
		if err != nil {
			return "", fmt.Errorf("read GPG_PRIVATE_KEY_FILE: %w", err)
		}
		return string(b), nil
	}
	if strings.TrimSpace(c.GpgPrivateKeyArmoredB64) != "" {
		raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(c.GpgPrivateKeyArmoredB64))
		if err != nil {
			return "", fmt.Errorf("decode GPG_PRIVATE_KEY_ARMORED_B64: %w", err)
		}
		return string(raw), nil
	}
	return c.GpgPrivateKey, nil
}

func envString(key, def string) string {
	v, ok := os.LookupEnv(key)
	if !ok {
		return def
	}
	return strings.TrimSpace(v)
}

// envBool parses 1/true/yes/on and 0/false/no/off; empty returns def.
func envBool(key string, def bool) bool {
	v := strings.ToLower(strings.TrimSpace(envString(key, "")))
	if v == "" {
		return def
	}
	switch v {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return def
	}
}

func envInt(key string, def int) int {
	v := envString(key, "")
	if v == "" {
		return def
	}
	i, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return i
}

func envDuration(key string, def time.Duration) time.Duration {
	v := envString(key, "")
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err == nil {
		return d
	}
	if n, err2 := strconv.Atoi(v); err2 == nil && n > 0 {
		return time.Duration(n) * time.Second
	}
	return def
}
