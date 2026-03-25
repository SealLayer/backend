package crypto

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// NormalizeArmoredPrivateKey fixes common PaaS issues: BOM, literal \n, CRLF.
func NormalizeArmoredPrivateKey(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "\ufeff")
	if !strings.Contains(s, "\n") && strings.Contains(s, `\n`) {
		s = strings.ReplaceAll(s, `\n`, "\n")
	}
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.TrimSpace(s)
}

// ImportArmoredPrivateKey imports an ASCII-armored secret key into the current GnuPG home (GNUPGHOME).
// passphrase may be empty if the key is not encrypted.
func ImportArmoredPrivateKey(ctx context.Context, armoredKey, passphrase string) error {
	armoredKey = NormalizeArmoredPrivateKey(armoredKey)
	if armoredKey == "" {
		return fmt.Errorf("armored private key is empty")
	}
	if !strings.Contains(armoredKey, "BEGIN PGP") {
		return fmt.Errorf("key material is not valid ASCII-armored OpenPGP (missing BEGIN PGP block); use multiline secret, a file mount (GPG_PRIVATE_KEY_FILE), or GPG_PRIVATE_KEY_ARMORED_B64")
	}

	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	var cmd *exec.Cmd
	if strings.TrimSpace(passphrase) != "" {
		cmd = exec.CommandContext(cctx, "gpg",
			"--batch", "--yes",
			"--pinentry-mode", "loopback",
			"--passphrase", passphrase,
			"--import",
		)
	} else {
		cmd = exec.CommandContext(cctx, "gpg", "--batch", "--yes", "--import")
	}
	cmd.Stdin = strings.NewReader(armoredKey)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("gpg --import: %w (%s)", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// SignDetachedArmorBase64 signs the given message bytes using GPG detached signature (ASCII armored),
// then returns base64 of that armored signature.
func SignDetachedArmorBase64(ctx context.Context, gpgKeyID, passphrase string, message []byte) (string, error) {
	if strings.TrimSpace(gpgKeyID) == "" {
		return "", fmt.Errorf("GPG_KEYID is empty")
	}

	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(cctx, "gpg",
		"--batch",
		"--yes",
		"--pinentry-mode", "loopback",
		"--passphrase", passphrase,
		"--local-user", gpgKeyID,
		"--detach-sign",
		"--armor",
	)
	cmd.Stdin = bytes.NewReader(message)
	var out bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("gpg sign error: %w (%s)", err, strings.TrimSpace(stderr.String()))
	}

	armored := out.Bytes()
	if len(armored) == 0 {
		return "", fmt.Errorf("gpg signature output empty")
	}
	return base64.StdEncoding.EncodeToString(armored), nil
}
