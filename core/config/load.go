package config

import (
	"fmt"
	"io"
	"net/url"
	"os"
	"reflect"
	"strconv"
	"strings"
	"time"
)

// maxSecretFileSize caps the bytes read from a _FILE secret source
// (audit FR-039). 1 MiB is comfortably above any realistic
// credential — JWTs, API tokens, certs, and passwords all fit inside
// 64 KiB; 1 MiB leaves margin for unusual deployment shapes (e.g.
// a multi-cert PEM bundle) without permitting accidental reads of
// large log files.
const maxSecretFileSize int64 = 1 << 20

// Load reads environment variables into a struct T using struct tags.
//
// Supported tags:
//   - env:"VAR_NAME"           — environment variable name
//   - env:"VAR_NAME,required"  — error if not set and no default
//   - default:"value"          — default when env var is empty
//   - secret:"true"            — also checks VAR_NAME_FILE (reads file contents)
//   - required:"true"          — on pointer-to-struct fields, error if no env vars were set
//
// Supported field types: string, int, int64, uint, uint8, uint16, uint32, uint64, bool,
// time.Duration, []string (comma-separated), *url.URL, float64.
//
// Note on []string: values are comma-separated and trimmed. Empty elements
// are rejected, so "a,,b" is a configuration error rather than ["a","b"].
//
// Nested structs are recursively loaded. Unexported fields are skipped.
func Load[T any]() (T, error) {
	var cfg T
	v := reflect.ValueOf(&cfg).Elem()
	// FR-040 [LOW]: reject pointer T early — hasEnvTags accepts
	// pointer receivers, but the loader assumes a struct and would
	// panic on .NumField() during nested-struct traversal. A typed
	// error makes the caller-facing failure mode obvious.
	if v.Kind() == reflect.Pointer {
		return cfg, fmt.Errorf("config: Load[T] requires T to be a struct type, not a pointer")
	}
	if v.Kind() != reflect.Struct {
		return cfg, fmt.Errorf("config: Load[T] requires T to be a struct type")
	}
	if !hasEnvTags(v.Type()) {
		return cfg, fmt.Errorf("config: type has no env struct tags — fields will be zero-valued")
	}
	if err := load(v); err != nil {
		return cfg, err
	}
	return cfg, nil
}

// MustLoad calls Load and panics on error. Use in main() for fail-fast.
func MustLoad[T any]() T {
	cfg, err := Load[T]()
	if err != nil {
		panic("config: Load failed")
	}
	return cfg
}

func load(v reflect.Value) error {
	_, err := loadWithEnvTracking(v)
	return err
}

// hasEnvTags checks whether a struct type (or any nested struct within it)
// has at least one field with an "env" struct tag. This prevents silent
// misconfiguration when Load is called with a type that has no tags.
func hasEnvTags(t reflect.Type) bool {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return false
	}
	for i := range t.NumField() {
		field := t.Field(i)
		if !field.IsExported() {
			continue
		}
		if field.Tag.Get("env") != "" {
			return true
		}
		ft := field.Type
		if ft.Kind() == reflect.Pointer {
			ft = ft.Elem()
		}
		if ft.Kind() == reflect.Struct && hasEnvTags(ft) {
			return true
		}
	}
	return false
}

// loadWithEnvTracking recursively loads environment variables into the struct
// and returns true if any env var was actually read from the environment
// (as opposed to only defaults being applied).
func loadWithEnvTracking(v reflect.Value) (envRead bool, _ error) {
	t := v.Type()
	for i := range t.NumField() {
		field := t.Field(i)
		if !field.IsExported() {
			continue
		}

		fv := v.Field(i)

		// Recurse into nested structs without an env tag.
		if field.Type.Kind() == reflect.Struct && field.Tag.Get("env") == "" {
			childRead, err := loadWithEnvTracking(fv)
			if err != nil {
				return false, err
			}
			if childRead {
				envRead = true
			}
			continue
		}

		// Recurse into pointer-to-struct fields without an env tag.
		// Only allocate the pointer if at least one nested field received a
		// value from the environment, preserving nil-means-disabled convention.
		if field.Type.Kind() == reflect.Pointer && field.Type.Elem().Kind() == reflect.Struct && field.Tag.Get("env") == "" {
			tmp := reflect.New(field.Type.Elem())
			childRead, err := loadWithEnvTracking(tmp.Elem())
			if err != nil {
				return false, err
			}
			if childRead {
				fv.Set(tmp)
				envRead = true
			} else if field.Tag.Get("required") == "true" {
				return false, fmt.Errorf("config: required struct %s has no environment variables set", field.Name)
			}
			continue
		}

		envTag := field.Tag.Get("env")
		if envTag == "" {
			continue
		}

		envName, required, tagErr := parseEnvTag(envTag, field.Name)
		if tagErr != nil {
			return false, tagErr
		}
		defaultVal := field.Tag.Get("default")
		isSecret := field.Tag.Get("secret") == "true"

		val, fromEnv, resolveErr := resolveWithSource(envName, isSecret)
		if resolveErr != nil {
			return false, resolveErr
		}
		if fromEnv {
			envRead = true
		}
		// "required" means non-empty: if the env var was explicitly set to ""
		// (empty file, blank export), reject even though a default exists. The
		// default is for the unset case, not for "operator overrode it with
		// nothing" — that's the misconfig the required flag is meant to catch.
		if required && fromEnv && val == "" {
			return false, fmt.Errorf("config: required environment variable %s is set but empty", envName)
		}
		if val == "" {
			val = defaultVal
		}
		if val == "" && required {
			return false, fmt.Errorf("config: required environment variable %s is not set", envName)
		}
		if val == "" {
			continue
		}

		if err := setField(fv, val, envName, isSecret); err != nil {
			return false, err
		}
	}
	return envRead, nil
}

func parseEnvTag(tag, fieldName string) (envName string, required bool, _ error) {
	parts := strings.Split(tag, ",")
	envName = strings.TrimSpace(parts[0])
	if envName == "" {
		return "", false, fmt.Errorf("config: field env tag must name an environment variable")
	}
	for _, rawOpt := range parts[1:] {
		opt := strings.TrimSpace(rawOpt)
		switch opt {
		case "required":
			required = true
		default:
			return "", false, fmt.Errorf("config: field env tag has unknown option")
		}
	}
	return envName, required, nil
}

// resolveWithSource returns the value for an environment variable and whether
// it came from the environment (as opposed to being empty/unset).
//
// For secret fields, envName+"_FILE" is authoritative when set and the
// direct environment variable is consulted only when no file source is
// configured. There is an
// inherent TOCTOU gap between reading the env var and reading the file — if
// the file is replaced between these operations, the wrong content is loaded.
// This is a fundamental limitation of file-based secret injection (Docker
// secrets, Kubernetes volume mounts) and is accepted as-is. Kubernetes secret
// volumes use atomic symlink swaps that make the race window negligible.
func resolveWithSource(envName string, isSecret bool) (val string, fromEnv bool, _ error) {
	if isSecret {
		filePath := os.Getenv(envName + "_FILE")
		if filePath != "" {
			val, err := readSecretFile(filePath)
			if err != nil {
				return "", false, fmt.Errorf("config: failed to read secret file for %s: %w", envName, err)
			}
			return val, true, nil
		}
	}

	val, found := os.LookupEnv(envName)
	if found && val != "" {
		return val, true, nil
	}
	if found {
		// Env var is set but empty — treat as "explicitly set" for
		// nil-means-disabled tracking even though the value is empty.
		return "", true, nil
	}
	if !isSecret {
		return "", false, nil
	}
	return "", false, nil
}

func readSecretFile(filePath string) (string, error) {
	// FR-039 [MED]: cap the read at maxSecretFileSize so a
	// misconfigured _FILE path (e.g. pointing at /var/log/system.log)
	// cannot pull arbitrary bytes into memory at startup.
	f, err := os.Open(filePath)
	if err != nil {
		return "", fmt.Errorf("open failed")
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, maxSecretFileSize+1))
	if err != nil {
		return "", fmt.Errorf("read failed")
	}
	if int64(len(data)) > maxSecretFileSize {
		return "", fmt.Errorf("exceeds maximum size")
	}
	// Trim only trailing line terminators that secret-mounting tools add
	// (Docker, Kubernetes, Vault all append a single \n). strings.TrimSpace
	// would also strip meaningful interior/leading whitespace from a base64
	// secret or a password that legitimately ends in spaces — silent
	// corruption that's hard to debug.
	return strings.TrimRight(string(data), "\r\n"), nil
}

func setField(fv reflect.Value, val, envName string, isSecret bool) error {
	// Handle *url.URL pointer type.
	// Note: scheme validation is intentionally limited to requiring a non-empty
	// scheme and host. Callers that need specific schemes (e.g., http/https only)
	// should validate after Load. This avoids restricting use cases like
	// amqp://, redis://, or custom internal schemes.
	if fv.Type() == reflect.TypeOf((*url.URL)(nil)) {
		u, err := url.Parse(val)
		if err != nil {
			return fmt.Errorf("config: %s: invalid URL syntax", envName)
		}
		if u.Scheme == "" || u.Host == "" {
			return fmt.Errorf("config: %s: invalid URL: must include scheme and host", envName)
		}
		fv.Set(reflect.ValueOf(u))
		return nil
	}

	switch fv.Kind() {
	case reflect.String:
		fv.SetString(val)

	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		if fv.Type() == reflect.TypeOf(time.Duration(0)) {
			d, err := time.ParseDuration(val)
			if err != nil {
				return invalidParseError(envName, "duration", val, isSecret, err)
			}
			fv.Set(reflect.ValueOf(d))
		} else {
			n, err := strconv.ParseInt(val, 10, fv.Type().Bits())
			if err != nil {
				return invalidParseError(envName, "integer", val, isSecret, err)
			}
			fv.SetInt(n)
		}

	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		n, err := strconv.ParseUint(val, 10, fv.Type().Bits())
		if err != nil {
			return invalidParseError(envName, "unsigned integer", val, isSecret, err)
		}
		fv.SetUint(n)

	case reflect.Bool:
		b, err := strconv.ParseBool(val)
		if err != nil {
			return invalidParseError(envName, "boolean", val, isSecret, err)
		}
		fv.SetBool(b)

	case reflect.Float32, reflect.Float64:
		f, err := strconv.ParseFloat(val, fv.Type().Bits())
		if err != nil {
			return invalidParseError(envName, "float", val, isSecret, err)
		}
		fv.SetFloat(f)

	case reflect.Slice:
		if fv.Type().Elem().Kind() == reflect.String {
			parts := strings.Split(val, ",")
			items := make([]string, 0, len(parts))
			for i, p := range parts {
				s := strings.TrimSpace(p)
				if s == "" {
					return fmt.Errorf("config: %s: empty list item at position %d", envName, i+1)
				}
				items = append(items, s)
			}
			fv.Set(reflect.ValueOf(items))
		} else {
			return fmt.Errorf("config: %s: unsupported slice type %s", envName, fv.Type())
		}

	case reflect.Map:
		return fmt.Errorf("config: %s: map fields are not supported (use a struct or []string instead)", envName)

	case reflect.Interface:
		return fmt.Errorf("config: %s: interface fields are not supported", envName)

	default:
		return fmt.Errorf("config: %s: unsupported type %s", envName, fv.Type())
	}
	return nil
}

func invalidParseError(envName, kind, _ string, isSecret bool, _ error) error {
	if isSecret {
		return fmt.Errorf("config: %s: invalid %s [REDACTED]", envName, kind)
	}
	return fmt.Errorf("config: %s: invalid %s", envName, kind)
}
