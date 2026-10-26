package natsbackend

import (
	"errors"
	"strings"
)

// Subject validation errors.
var (
	ErrSubjectEmpty    = errors.New("natsbackend: subject is empty")
	ErrSubjectToken    = errors.New("natsbackend: subject has an empty token or a character NATS reserves")
	ErrSubjectWildcard = errors.New("natsbackend: subject has a wildcard")
	ErrFilterWildcard  = errors.New("natsbackend: filter has a misplaced wildcard")
)

// SubjectToken escapes value into one subject token. Every byte other than
// A–Z, a–z, 0–9, '-' and '_' is written as %XX, '%' included, so distinct
// values never share a token and [ParseSubjectToken] restores the value.
// The empty value is the token "%", which no other value escapes to.
func SubjectToken(value string) string {
	if value == "" {
		return "%"
	}
	var b strings.Builder
	for i := 0; i < len(value); i++ {
		c := value[i]
		if plain(c) {
			b.WriteByte(c)
			continue
		}
		b.WriteByte('%')
		b.WriteByte(hexDigits[c>>4])
		b.WriteByte(hexDigits[c&0xf])
	}
	return b.String()
}

const hexDigits = "0123456789ABCDEF"

func plain(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_'
}

// ParseSubjectToken restores the value [SubjectToken] escaped. A token it
// cannot have written is an error.
func ParseSubjectToken(token string) (string, error) {
	if token == "%" {
		return "", nil
	}
	if token == "" {
		return "", ErrSubjectToken
	}
	var b strings.Builder
	for i := 0; i < len(token); i++ {
		c := token[i]
		if plain(c) {
			b.WriteByte(c)
			continue
		}
		if c != '%' || i+2 >= len(token) {
			return "", ErrSubjectToken
		}
		hi, lo := strings.IndexByte(hexDigits, token[i+1]), strings.IndexByte(hexDigits, token[i+2])
		if hi < 0 || lo < 0 || plain(byte(hi<<4|lo)) {
			return "", ErrSubjectToken
		}
		b.WriteByte(byte(hi<<4 | lo))
		i += 2
	}
	return b.String(), nil
}

// Subject joins values into a subject of one token each, escaping every
// value with [SubjectToken]. It needs at least one value.
func Subject(values ...string) (string, error) {
	if len(values) == 0 {
		return "", ErrSubjectEmpty
	}
	tokens := make([]string, len(values))
	for i, v := range values {
		tokens[i] = SubjectToken(v)
	}
	return strings.Join(tokens, "."), nil
}

// ValidateSubject reports whether s is a subject a message can be
// published to: tokens separated by '.', none empty, no wildcard and no
// whitespace or control character.
func ValidateSubject(s string) error {
	return validate(s, false)
}

// ValidateFilter reports whether s is a subject filter: a subject whose
// tokens may also be '*' (one token) or, as the last token only, '>' (the
// rest).
func ValidateFilter(s string) error {
	return validate(s, true)
}

func validate(s string, wildcards bool) error {
	if s == "" {
		return ErrSubjectEmpty
	}
	tokens := strings.Split(s, ".")
	for i, token := range tokens {
		switch token {
		case "":
			return ErrSubjectToken
		case "*", ">":
			if !wildcards {
				return ErrSubjectWildcard
			}
			if token == ">" && i != len(tokens)-1 {
				return ErrFilterWildcard
			}
			continue
		}
		for j := 0; j < len(token); j++ {
			c := token[j]
			if c <= ' ' || c == 0x7f {
				return ErrSubjectToken
			}
			if c == '*' || c == '>' {
				if wildcards {
					return ErrFilterWildcard
				}
				return ErrSubjectWildcard
			}
		}
	}
	return nil
}
