package validate

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/mail"
	"net/url"
	"strconv"
	"strings"
	"unicode"

	"github.com/google/uuid"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

// builtinFormats returns the kit's default format set. Unknown
// parametric formats (e.g. `starts-with:/api`) are also registered so
// santhosh-tekuri can dispatch them. Caller passes the list of
// parametric formats it has seen during schema generation.
func builtinFormats(parametric []string) []*jsonschema.Format {
	out := []*jsonschema.Format{
		{Name: "email", Validate: validateEmail},
		{Name: "uri", Validate: validateURI},
		{Name: "uuid", Validate: validateUUID},
		{Name: "ipv4-or-ipv6", Validate: validateIP},
		{Name: "hostname", Validate: validateHostname},
		{Name: "alpha", Validate: validateAlpha},
		{Name: "alphanum", Validate: validateAlphanum},
		{Name: "numeric", Validate: validateNumeric},
		{Name: "cidr", Validate: validateCIDR},
	}
	seen := map[string]struct{}{}
	for _, name := range parametric {
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, parametricFormat(name))
	}
	return out
}

func validateEmail(v any) error {
	s, ok := v.(string)
	if !ok {
		return errors.New("not a string")
	}
	if s == "" {
		// Defer empty-string handling to minLength / required so
		// the message stays "is required" rather than "must be a
		// valid email address" on a missing required field.
		return nil
	}
	_, err := mail.ParseAddress(s)
	return err
}

func validateURI(v any) error {
	s, ok := v.(string)
	if !ok {
		return errors.New("not a string")
	}
	if s == "" {
		return nil
	}
	u, err := url.Parse(s)
	if err != nil {
		return err
	}
	if u.Scheme == "" || u.Host == "" {
		return errors.New("missing scheme or host")
	}
	return nil
}

func validateUUID(v any) error {
	s, ok := v.(string)
	if !ok {
		return errors.New("not a string")
	}
	if s == "" {
		return nil
	}
	_, err := uuid.Parse(s)
	return err
}

func validateIP(v any) error {
	s, ok := v.(string)
	if !ok {
		return errors.New("not a string")
	}
	if s == "" {
		return nil
	}
	if net.ParseIP(s) == nil {
		return errors.New("not a valid IP")
	}
	return nil
}

func validateHostname(v any) error {
	s, ok := v.(string)
	if !ok {
		return errors.New("not a string")
	}
	if s == "" {
		return nil
	}
	if len(s) > 253 {
		return errors.New("hostname length out of range")
	}
	for _, label := range strings.Split(s, ".") {
		if len(label) == 0 || len(label) > 63 {
			return errors.New("invalid hostname label")
		}
		for i, r := range label {
			switch {
			case unicode.IsLetter(r) || unicode.IsDigit(r):
				// ok
			case r == '-' && i != 0 && i != len(label)-1:
				// ok
			default:
				return errors.New("invalid hostname character")
			}
		}
	}
	return nil
}

func validateAlpha(v any) error {
	s, ok := v.(string)
	if !ok {
		return errors.New("not a string")
	}
	if s == "" {
		return nil
	}
	for _, r := range s {
		if !unicode.IsLetter(r) {
			return errors.New("non-letter character")
		}
	}
	return nil
}

func validateAlphanum(v any) error {
	s, ok := v.(string)
	if !ok {
		return errors.New("not a string")
	}
	if s == "" {
		return nil
	}
	for _, r := range s {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			return errors.New("non-alphanumeric character")
		}
	}
	return nil
}

func validateNumeric(v any) error {
	switch n := v.(type) {
	case float64, float32, int, int64, json.Number:
		_ = n
		return nil
	case string:
		if n == "" {
			return nil
		}
		if _, err := strconv.ParseFloat(n, 64); err == nil {
			return nil
		}
		return errors.New("not numeric")
	}
	return errors.New("not numeric")
}

func validateCIDR(v any) error {
	s, ok := v.(string)
	if !ok {
		return errors.New("not a string")
	}
	if s == "" {
		return nil
	}
	if _, _, err := net.ParseCIDR(s); err != nil {
		return err
	}
	return nil
}

// parametricFormat constructs a santhosh-tekuri Format for a
// kit-specific parametric name like `starts-with:/api`. The leaf
// dispatch reads the substring after the prefix.
func parametricFormat(name string) *jsonschema.Format {
	switch {
	case strings.HasPrefix(name, "starts-with:"):
		want := strings.TrimPrefix(name, "starts-with:")
		return &jsonschema.Format{
			Name: name,
			Validate: func(v any) error {
				s, ok := v.(string)
				if !ok {
					return errors.New("not a string")
				}
				if s == "" {
					return nil
				}
				if !strings.HasPrefix(s, want) {
					return fmt.Errorf("does not start with %q", want)
				}
				return nil
			},
		}
	case strings.HasPrefix(name, "ends-with:"):
		want := strings.TrimPrefix(name, "ends-with:")
		return &jsonschema.Format{
			Name: name,
			Validate: func(v any) error {
				s, ok := v.(string)
				if !ok {
					return errors.New("not a string")
				}
				if s == "" {
					return nil
				}
				if !strings.HasSuffix(s, want) {
					return fmt.Errorf("does not end with %q", want)
				}
				return nil
			},
		}
	case strings.HasPrefix(name, "contains:"):
		want := strings.TrimPrefix(name, "contains:")
		return &jsonschema.Format{
			Name: name,
			Validate: func(v any) error {
				s, ok := v.(string)
				if !ok {
					return errors.New("not a string")
				}
				if s == "" {
					return nil
				}
				if !strings.Contains(s, want) {
					return fmt.Errorf("does not contain %q", want)
				}
				return nil
			},
		}
	case strings.HasPrefix(name, "excludes-all:"):
		chars := strings.TrimPrefix(name, "excludes-all:")
		return &jsonschema.Format{
			Name: name,
			Validate: func(v any) error {
				s, ok := v.(string)
				if !ok {
					return errors.New("not a string")
				}
				if s == "" {
					return nil
				}
				if strings.ContainsAny(s, chars) {
					return errors.New("contains disallowed character")
				}
				return nil
			},
		}
	}
	// Unknown parametric — accept any value so absence of the format
	// does not turn into a runtime panic. Callers registering their
	// own parametric format should call RegisterFormat with the
	// fully-parametrised name.
	return &jsonschema.Format{Name: name, Validate: func(any) error { return nil }}
}
