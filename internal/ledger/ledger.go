package ledger

import (
	"bufio"
	"encoding/json"
	"errors"
	"strings"
)

type Row struct {
	ID          string `json:"id"`
	ContentHash string `json:"content_hash"`
	PrevHash    string `json:"prev_hash"`
	TS          int64  `json:"ts"`
	FinalHash   string `json:"final_hash"`
}

func ParseLastFinalHash(jsonl string) (string, error) {
	sc := bufio.NewScanner(strings.NewReader(jsonl))
	var last string
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var r Row
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			continue
		}
		if r.FinalHash != "" {
			last = r.FinalHash
		}
	}
	if err := sc.Err(); err != nil {
		return "", err
	}
	if last == "" {
		return "", errors.New("no final_hash found in ledger")
	}
	return last, nil
}

func AppendRows(jsonl string, rows []Row) (string, error) {
	var b strings.Builder
	if strings.TrimSpace(jsonl) != "" {
		b.WriteString(strings.TrimRight(jsonl, "\n"))
		b.WriteString("\n")
	}
	for i, r := range rows {
		raw, err := json.Marshal(r)
		if err != nil {
			return "", err
		}
		b.Write(raw)
		if i != len(rows)-1 {
			b.WriteString("\n")
		}
	}
	b.WriteString("\n")
	return b.String(), nil
}
