package messaging

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/bds421/rho-kit/io/v2/atomicfile"
)

// State-file format (append-only journal)
//
// The state file is line-oriented (NDJSON). The FIRST line is a JSON array
// holding the snapshot base — the set of pending messages captured at the
// last compaction (possibly an empty array). Each SUBSEQUENT non-empty line
// is a single JSON-encoded [pendingMessage] appended after that snapshot.
// Replay = snapshot base ++ appended entries, in file order.
//
// This shape is BACKWARD-COMPATIBLE with the pre-journal format, which was a
// single JSON array written by [atomicfile.Save]: such a file is exactly a
// journal whose first (and only) line is the snapshot base with zero appended
// entries. [loadJournal] therefore restores both an old single-array snapshot
// and a new journal without distinguishing them at the call site — an
// in-flight upgrade never loses buffered messages.
//
// Why a journal: a full-snapshot rewrite on every buffered Publish costs
// O(n) bytes per message, i.e. O(n^2) total during a broker-outage burst.
// Appending only the new entry is O(1) per Publish; the file is compacted
// back to a single snapshot line on drain / when the buffer empties / once
// the journal tail grows past a threshold, keeping replay bounded.

const (
	// bufferedJournalCompactMinEntries is the floor before the append-vs-
	// snapshot heuristic can choose compaction. Below it the journal tail is
	// negligible and a snapshot rewrite would only add churn.
	bufferedJournalCompactMinEntries = 64

	// bufferedJournalCompactBytes force-compacts once the appended tail grows
	// past this many bytes, independent of entry count. This bounds the extra
	// bytes a restart must replay and keeps the whole file comfortably under
	// [atomicfile.MaxLoadBytes].
	bufferedJournalCompactBytes = 1 << 20 // 1 MiB
)

// marshalPendingEntry renders a single pending entry as one journal line
// (compact JSON, no trailing newline). The caller appends the newline so the
// line framing stays in one place ([appendPendingEntry]).
func marshalPendingEntry(pm pendingMessage) ([]byte, error) {
	data, err := json.Marshal(pm)
	if err != nil {
		return nil, fmt.Errorf("marshal pending entry: %w", err)
	}
	if bytes.IndexByte(data, '\n') >= 0 {
		// json.Marshal never emits a raw newline in compact output, so this
		// is defensive only; a newline would corrupt line framing on replay.
		return nil, errors.New("marshalled pending entry contains a newline")
	}
	return data, nil
}

// appendPendingEntry appends one already-marshalled journal line to path and
// fsyncs it, preserving the per-append durability the full-snapshot writer
// provided. The file must already exist as a snapshot written by
// [atomicfile.Save] (which creates the parent directory and fsyncs the
// directory entry); appends only ever touch an existing file, so the directory
// entry is already durable and no second directory fsync is required.
//
// [atomicfile.Save] writes the snapshot array with no trailing newline, so the
// FIRST append must insert the line separator itself; otherwise the snapshot
// and the first entry would share a line and corrupt replay. We detect this by
// checking the current end-of-file byte rather than tracking state, so the
// separator is added exactly once regardless of how many appends follow.
func appendPendingEntry(path string, line []byte) error {
	needsLeadingNewline, err := fileNeedsLeadingNewline(path)
	if err != nil {
		return err
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open journal for append: %w", err)
	}

	framed := make([]byte, 0, len(line)+2)
	if needsLeadingNewline {
		framed = append(framed, '\n')
	}
	framed = append(framed, line...)
	framed = append(framed, '\n')

	if _, err := f.Write(framed); err != nil {
		_ = f.Close()
		return fmt.Errorf("append journal entry: %w", err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return fmt.Errorf("fsync journal entry: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close journal: %w", err)
	}
	return nil
}

// fileNeedsLeadingNewline reports whether an append to path must be prefixed
// with a newline because the file is non-empty and does not already end in one
// (the case immediately after [atomicfile.Save] wrote a snapshot array).
func fileNeedsLeadingNewline(path string) (bool, error) {
	info, err := os.Stat(path)
	if err != nil {
		return false, fmt.Errorf("stat journal: %w", err)
	}
	if info.Size() == 0 {
		return false, nil
	}

	f, err := os.Open(path)
	if err != nil {
		return false, fmt.Errorf("open journal: %w", err)
	}
	defer func() { _ = f.Close() }()

	last := make([]byte, 1)
	if _, err := f.ReadAt(last, info.Size()-1); err != nil {
		return false, fmt.Errorf("read journal tail: %w", err)
	}
	return last[0] != '\n', nil
}

// loadJournal reads pending messages from path, accepting both the legacy
// single-array snapshot and the append-only journal.
//
// It first tries the fast path — decode the whole file as one JSON array via
// [atomicfile.LoadOrZero], which also handles a missing file (returns nil) and
// inherits atomicfile's symlink and size protections. A pure snapshot (legacy
// or freshly compacted) and an empty file both succeed there unchanged.
//
// A journal with appended entries fails the whole-file array decode (trailing
// data after the array), so on that decode error the file is re-read and parsed
// line-by-line: line 0 is the snapshot base array, each remaining non-empty
// line is one [pendingMessage]. A genuinely corrupt file (neither a valid array
// nor a valid first-line array) surfaces the parse error to the caller, who
// fails closed unless [WithLossyStateRecovery] is set.
func loadJournal(path string) ([]pendingMessage, error) {
	snapshot, err := atomicfile.LoadOrZero[[]pendingMessage](path)
	if err == nil {
		return snapshot, nil
	}

	// The whole-file array decode failed. This is the expected outcome for a
	// journal carrying appended entries; fall back to line-oriented parsing.
	parsed, journalErr := parseJournalFile(path)
	if journalErr != nil {
		// Neither interpretation worked: report the original (snapshot) error
		// so corruption surfaces with the same message callers already pin.
		return nil, err
	}
	return parsed, nil
}

// parseJournalFile parses path as a line-oriented journal: the first non-empty
// line is the snapshot base array; each remaining non-empty line is a single
// pending entry. The byte budget mirrors [atomicfile.MaxLoadBytes] so a
// runaway file cannot OOM the loader.
func parseJournalFile(path string) ([]pendingMessage, error) {
	if err := rejectStateSymlink(path); err != nil {
		return nil, err
	}

	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("stat journal: %w", err)
	}
	if info.Size() > atomicfile.MaxLoadBytes {
		return nil, fmt.Errorf("journal exceeds %d bytes (got %d)", atomicfile.MaxLoadBytes, info.Size())
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read journal: %w", err)
	}

	lines := bytes.Split(data, []byte{'\n'})

	// First non-empty line is the snapshot base.
	idx := 0
	for idx < len(lines) && len(bytes.TrimSpace(lines[idx])) == 0 {
		idx++
	}
	if idx >= len(lines) {
		return nil, errors.New("journal has no snapshot line")
	}

	var base []pendingMessage
	if err := json.Unmarshal(lines[idx], &base); err != nil {
		return nil, fmt.Errorf("decode journal snapshot line: %w", err)
	}
	idx++

	// Collect the appended entry lines (skipping blank framing lines) so a
	// torn final write can be distinguished from interior corruption.
	entryLines := make([][]byte, 0, len(lines)-idx)
	for ; idx < len(lines); idx++ {
		line := bytes.TrimSpace(lines[idx])
		if len(line) == 0 {
			continue
		}
		entryLines = append(entryLines, line)
	}

	pending := make([]pendingMessage, 0, len(base)+len(entryLines))
	pending = append(pending, base...)

	for i, line := range entryLines {
		var pm pendingMessage
		if err := json.Unmarshal(line, &pm); err != nil {
			// A decode failure on the FINAL entry is a torn trailing append:
			// the writer crashed or hit a write error mid-line, so this
			// message was never acknowledged to the caller (Publish returned
			// an error and rolled it back). Dropping it is correct recovery,
			// not silent loss. A failure on any EARLIER line is genuine
			// interior corruption and stays fatal, matching the strict
			// "loud, not silent" default for state-file damage.
			if i == len(entryLines)-1 {
				break
			}
			return nil, fmt.Errorf("decode journal entry %d: %w", i, err)
		}
		pending = append(pending, pm)
	}

	return pending, nil
}

// rejectStateSymlink refuses to read the state file through a symlink at the
// final path component, mirroring the destination check [atomicfile.Save]
// performs before writing. Ancestor containment is already enforced by the
// constructor (resolveStateFilePath) and by atomicfile on every write.
func rejectStateSymlink(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("stat journal: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return errors.New("refusing to read journal through symlink")
	}
	return nil
}
