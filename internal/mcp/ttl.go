package mcp

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"legado-go/internal/model"
)

// parseTTL 接受 {"ttl":"2m"}、{"ttl":120} 或 {"ttlSeconds":120}。
func parseTTL(raw json.RawMessage, seconds int) (time.Duration, error) {
	if len(raw) > 0 && string(raw) != "null" {
		var s string
		if json.Unmarshal(raw, &s) == nil {
			s = strings.TrimSpace(s)
			if s == "" {
				return 0, nil
			}
			if n, err := strconv.Atoi(s); err == nil {
				return time.Duration(n) * time.Second, nil
			}
			d, err := time.ParseDuration(s)
			if err != nil {
				return 0, model.WrapError(model.KindInvalidConfig, err, "invalid ttl: %s", s)
			}
			return d, nil
		}
		var n int
		if json.Unmarshal(raw, &n) == nil {
			return time.Duration(n) * time.Second, nil
		}
		return 0, model.WrapError(model.KindInvalidConfig, nil, "invalid ttl")
	}
	return time.Duration(seconds) * time.Second, nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
