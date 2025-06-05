package utils

import (
	"golang.org/x/text/runes"
	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"
	"strings"
	"unicode"
)

// NormalizeString converts to lowercase and removes accents.
func NormalizeString(s string) string {
	s = strings.ToLower(s)
	s = strings.TrimSpace(s)
	t := transform.Chain(norm.NFD, runes.Remove(runes.In(unicode.Mn)), norm.NFC)
	result, _, _ := transform.String(t, s)
	return result
}
