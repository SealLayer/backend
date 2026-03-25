package crypto

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
)

func Sha256HexLower(input string) string {
	sum := sha256.Sum256([]byte(input))
	return hex.EncodeToString(sum[:])
}

func FinalHash(contentHash, prevHash string, ts int64) string {
	tsString := strconv.FormatInt(ts, 10)
	return Sha256HexLower(contentHash + prevHash + tsString)
}

func BatchRoot(finalHashes []string) string {
	concat := ""
	for _, h := range finalHashes {
		concat += h
	}
	return Sha256HexLower(concat)
}
