package auditlog

import (
	"context"
	"time"
)

// RetentionRecord returns the sealed record of what [MemoryStore.ApplyCut]
// removed.
func (m *MemoryStore) RetentionRecord(ctx context.Context, key []byte) (RetentionRecord, error) {
	if err := ctx.Err(); err != nil {
		return RetentionRecord{}, err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.sealedRecord(key)
}

// ApplyCut removes the events c names and replaces the sealed record, as
// [RetentionStore] describes.
func (m *MemoryStore) ApplyCut(ctx context.Context, c Cut, key []byte, minimum time.Duration, now time.Time) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if err := c.Removable(minimum, now); err != nil {
		return 0, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	earlier, err := m.sealedRecord(key)
	if err != nil || c.Events == 0 {
		return 0, err
	}
	if int64(len(m.events)) <= c.Events {
		return 0, ErrCutRefused
	}
	hmacs := make([][]byte, c.Events+1)
	for i := range hmacs {
		hmacs[i] = m.events[i].HMAC
	}
	if err := c.Holds(hmacs); err != nil {
		return 0, err
	}
	next := earlier.After(c, now)
	seal, err := next.Seal(key)
	if err != nil {
		return 0, err
	}
	m.events = append([]Event(nil), m.events[c.Events:]...)
	m.record, m.seal = next, seal
	return c.Events, nil
}

func (m *MemoryStore) sealedRecord(key []byte) (RetentionRecord, error) {
	if m.seal == nil {
		if _, err := m.record.Seal(key); err != nil {
			return RetentionRecord{}, err
		}
		return RetentionRecord{}, nil
	}
	if err := m.record.CheckSeal(key, m.seal); err != nil {
		return RetentionRecord{}, err
	}
	r := m.record
	r.Watermark = append([]byte(nil), r.Watermark...)
	return r, nil
}
