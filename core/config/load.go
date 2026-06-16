package config

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"reflect"
	"strconv"
	"strings"
	"syscall"
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
//
// The panic value embeds the underlying Load error so the operator
// sees which env var / file source caused the failure. Wave 68
// closed a hostile-review finding that the prior generic message
// discarded the cause.
func MustLoad[T any]() T {
	cfg, err := Load[T]()
	if err != nil {
		panic(fmt.Sprintf("config: Load failed: %s", err))
	}
	return cfg
}

func load(v reflect.Value) error {
	_, err := loadWithEnvTracking(v, map[reflect.Type]struct{}{})
	return err
}

// ErrConfigCycle is returned by [Load] when the config type T (or a nested
// type) refers back to itself through a struct or pointer-to-struct field.
// Such a type cannot be loaded because recursion would never terminate.
var ErrConfigCycle = errors.New("config: type contains a recursive (self-referential) struct field")

// hasEnvTags checks whether a struct type (or any nested struct within it)
// has at least one field with an "env" struct tag. This prevents silent
// misconfiguration when Load is called with a type that has no tags.
//
// Self-referential types (a struct that reaches itself through a struct or
// pointer-to-struct field) are walked with a visited set so the pre-check
// terminates instead of overflowing the stack; the cycle itself contributes
// no new tagged fields, so a revisited type is treated as "no further tags".
func hasEnvTags(t reflect.Type) bool {
	return hasEnvTagsVisited(t, map[reflect.Type]struct{}{})
}

func hasEnvTagsVisited(t reflect.Type, visited map[reflect.Type]struct{}) bool {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return false
	}
	if _, seen := visited[t]; seen {
		return false
	}
	visited[t] = struct{}{}
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
		if ft.Kind() == reflect.Struct && hasEnvTagsVisited(ft, visited) {
			return true
		}
	}
	return false
}

// loadWithEnvTracking recursively loads environment variables into the struct
// and returns true if any env var was actually read from the environment
// (as opposed to only defaults being applied).
//
// visited records the struct types currently on the recursion stack. A
// self-referential type (e.g. `type Node struct { Next *Node }`) would
// otherwise recurse forever and crash the process with an unrecoverable
// stack overflow; instead we return [ErrConfigCycle].
func loadWithEnvTracking(v reflect.Value, visited map[reflect.Type]struct{}) (envRead bool, _ error) {
	t := v.Type()
	if _, seen := visited[t]; seen {
		return false, ErrConfigCycle
	}
	visited[t] = struct{}{}
	defer delete(visited, t)
	for i := range t.NumField() {
		field := t.Field(i)
		if !field.IsExported() {
			continue
		}

		fv := v.Field(i)

		// Recurse into nested structs without an env tag.
		if field.Type.Kind() == reflect.Struct && field.Tag.Get("env") == "" {
			childRead, err := loadWithEnvTracking(fv, visited)
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
			childRead, err := loadWithEnvTracking(tmp.Elem(), visited)
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

		val, valBytes, fromEnv, resolveErr := resolveWithSource(envName, isSecret)
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
			if valBytes != nil {
				zeroBytes(valBytes)
			}
			return false, fmt.Errorf("config: required environment variable %s is set but empty", envName)
		}
		if val == "" {
			val = defaultVal
		}
		if val == "" && required {
			if valBytes != nil {
				zeroBytes(valBytes)
			}
			return false, fmt.Errorf("config: required environment variable %s is not set", envName)
		}
		if val == "" {
			if valBytes != nil {
				zeroBytes(valBytes)
			}
			continue
		}

		err := setField(fv, val, envName, isSecret)
		if valBytes != nil {
			// Zero the originating bytes regardless of setField outcome;
			// the value has either been copied (string fields) or
			// parsed into a typed field by this point.
			zeroBytes(valBytes)
		}
		if err != nil {
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
//
// For _FILE-sourced secrets, the on-disk bytes are read into a []byte that
// the caller zeroes after the value has been parsed / copied into its final
// destination. Note this only bounds the lifetime of that intermediate file
// buffer: the value is first converted with string(b) below, so an immutable
// heap copy of the secret already exists by the time the buffer is zeroed, and
// that string lives for the program's lifetime when the target is a string
// field (Lens F A.9).
func resolveWithSource(envName string, isSecret bool) (val string, valBytes []byte, fromEnv bool, _ error) {
	if isSecret {
		filePath := os.Getenv(envName + "_FILE")
		if filePath != "" {
			b, err := readSecretFile(filePath)
			if err != nil {
				return "", nil, false, fmt.Errorf("config: failed to read secret file for %s: %w", envName, err)
			}
			return string(b), b, true, nil
		}
	}

	v, found := os.LookupEnv(envName)
	if found && v != "" {
		return v, nil, true, nil
	}
	if found {
		// Env var is set but empty — treat as "explicitly set" for
		// nil-means-disabled tracking even though the value is empty.
		return "", nil, true, nil
	}
	if !isSecret {
		return "", nil, false, nil
	}
	return "", nil, false, nil
}

// zeroBytes overwrites the slice with zeros. Inlines the same body as
// crypto/encrypt to keep core/config dependency-free.
func zeroBytes(b []byte) {
	for i := range b {
		b[i] = 0
	}
}

// readSecretFile reads a _FILE-sourced secret as []byte so the
// originating buffer can be zeroed after the value is parsed. Errors
// are wrapped so callers can use errors.Is against [fs.ErrPermission],
// [fs.ErrNotExist], and [fs.ErrInvalid] to distinguish typo vs
// permissions vs unreadable target — the file path itself is never
// included in the returned error (Lens F A.16).
func readSecretFile(filePath string) ([]byte, error) {
	// FR-039 [MED]: cap the read at maxSecretFileSize so a
	// misconfigured _FILE path (e.g. pointing at /var/log/system.log)
	// cannot pull arbitrary bytes into memory at startup.
	f, err := os.Open(filePath)
	if err != nil {
		// Wrap the os-level cause so callers can errors.Is against
		// fs.ErrPermission / fs.ErrNotExist; the path is REDACTED
		// from the message so secrets directories don't leak.
		return nil, classifyOpenError(err)
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, maxSecretFileSize+1))
	if err != nil {
		// Use classifyOpenError so the resulting error is errors.Is-comparable
		// against fs sentinels (a directory read raises EISDIR which surfaces
		// as fs.ErrInvalid). The path is REDACTED.
		return nil, classifyReadError(err)
	}
	if int64(len(data)) > maxSecretFileSize {
		// Zero the over-large buffer before returning — operators
		// expect secret material in the file even if it's mis-sized.
		zeroBytes(data)
		return nil, fmt.Errorf("exceeds maximum size: %w", fs.ErrInvalid)
	}
	// Trim only trailing line terminators that secret-mounting tools add
	// (Docker, Kubernetes, Vault all append a single \n). strings.TrimSpace
	// would also strip meaningful interior/leading whitespace from a base64
	// secret or a password that legitimately ends in spaces — silent
	// corruption that's hard to debug.
	trimmed := trimTrailingLineEndings(data)
	if len(trimmed) < len(data) {
		// Zero the bytes we trimmed off so no fragment lingers.
		zeroBytes(data[len(trimmed):])
	}
	return trimmed, nil
}

// trimTrailingLineEndings returns a sub-slice of b with trailing CR/LF
// bytes removed. The returned slice aliases b so callers that zero the
// returned slice also zero the originating storage for those bytes.
func trimTrailingLineEndings(b []byte) []byte {
	end := len(b)
	for end > 0 && (b[end-1] == '\n' || b[end-1] == '\r') {
		end--
	}
	return b[:end]
}

// classifyOpenError maps os.Open errors into a wrapped form that
// preserves errors.Is against the standard fs sentinels while keeping
// the file path REDACTED.
func classifyOpenError(err error) error {
	switch {
	case errors.Is(err, fs.ErrPermission):
		return fmt.Errorf("open failed: %w", fs.ErrPermission)
	case errors.Is(err, fs.ErrNotExist):
		return fmt.Errorf("open failed: %w", fs.ErrNotExist)
	case errors.Is(err, fs.ErrInvalid):
		return fmt.Errorf("open failed: %w", fs.ErrInvalid)
	default:
		// Unknown causes are flattened to a generic message; the path
		// must never leak into the surfaced error.
		return errors.New("open failed")
	}
}

// classifyReadError maps io.ReadAll errors into the same redacted
// form as classifyOpenError. Reading a directory raises EISDIR which
// is platform-specific (a syscall.Errno on Unix); we normalise that
// — and any other invalid-target read — onto fs.ErrInvalid so callers
// can use a single sentinel.
func classifyReadError(err error) error {
	switch {
	case errors.Is(err, fs.ErrPermission):
		return fmt.Errorf("read failed: %w", fs.ErrPermission)
	case errors.Is(err, fs.ErrNotExist):
		return fmt.Errorf("read failed: %w", fs.ErrNotExist)
	case errors.Is(err, fs.ErrInvalid), errors.Is(err, syscall.EISDIR):
		return fmt.Errorf("read failed: %w", fs.ErrInvalid)
	default:
		// Generic read error — strip the path / unwrapped cause so
		// neither the operator's filesystem layout nor any internal
		// errno detail rides along.
		return errors.New("read failed")
	}
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
