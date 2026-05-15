package jmap

import "strings"

const redacted = "<redacted>"

func Redact(value string, secrets ...string) string {
	out := value
	for _, secret := range secrets {
		if secret == "" {
			continue
		}
		out = strings.ReplaceAll(out, secret, redacted)
	}
	for _, marker := range []string{"Authorization:", "authorization:"} {
		out = redactHeaderLine(out, marker)
	}
	out = redactJSONPassword(out)
	return out
}

func redactHeaderLine(value, marker string) string {
	idx := strings.Index(value, marker)
	for idx >= 0 {
		lineEnd := strings.IndexByte(value[idx:], '\n')
		end := len(value)
		if lineEnd >= 0 {
			end = idx + lineEnd
		}
		value = value[:idx+len(marker)] + " " + redacted + value[end:]
		next := strings.Index(value[idx+len(marker)+len(redacted):], marker)
		if next < 0 {
			break
		}
		idx = idx + len(marker) + len(redacted) + next
	}
	return value
}

func redactJSONPassword(value string) string {
	for _, key := range []string{"\"password\"", "\"Password\""} {
		idx := strings.Index(value, key)
		for idx >= 0 {
			colon := strings.IndexByte(value[idx+len(key):], ':')
			if colon < 0 {
				break
			}
			start := idx + len(key) + colon + 1
			for start < len(value) && (value[start] == ' ' || value[start] == '\t') {
				start++
			}
			if start >= len(value) || value[start] != '"' {
				break
			}
			end := start + 1
			for end < len(value) {
				if value[end] == '"' && value[end-1] != '\\' {
					break
				}
				end++
			}
			if end >= len(value) {
				break
			}
			value = value[:start+1] + redacted + value[end:]
			next := strings.Index(value[start+len(redacted):], key)
			if next < 0 {
				break
			}
			idx = start + len(redacted) + next
		}
	}
	return value
}
