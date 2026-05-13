package config

import (
	"log"
	"sync"
	"time"

	"github.com/mailhog/data"
	"github.com/mailhog/storage"
)

// ManagedStorage wraps a storage backend and applies retention policy.
type ManagedStorage struct {
	base                  storage.Storage
	cache                 storage.Storage
	cacheEnabled          bool
	mu                    sync.RWMutex
	retentionDays         int
	lastRetentionSweep    time.Time
	retentionSweepInterval time.Duration
}

func NewManagedStorage(base storage.Storage, retentionDays int, enableMemoryCache bool) *ManagedStorage {
	if retentionDays <= 0 {
		retentionDays = 10
	}
	m := &ManagedStorage{
		base:                   base,
		retentionDays:          retentionDays,
		retentionSweepInterval: time.Minute,
	}
	if enableMemoryCache {
		m.cache = storage.CreateInMemory()
		if err := m.rebuildCache(); err != nil {
			log.Printf("ManagedStorage cache warmup failed, falling back to base storage reads: %s", err)
			m.cache = nil
			m.cacheEnabled = false
		} else {
			m.cacheEnabled = true
			log.Printf("ManagedStorage memory cache enabled")
		}
	}
	return m
}

func (m *ManagedStorage) Store(msg *data.Message) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	id, err := m.base.Store(msg)
	if err != nil {
		return id, err
	}
	if m.cacheEnabled {
		if _, cacheErr := m.cache.Store(msg); cacheErr != nil {
			log.Printf("ManagedStorage cache store failed for message %s: %s", id, cacheErr)
		}
	}
	return id, nil
}

func (m *ManagedStorage) List(start, limit int) (*data.Messages, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.cacheEnabled {
		return m.cache.List(start, limit)
	}
	return m.base.List(start, limit)
}

func (m *ManagedStorage) Search(kind, query string, start, limit int) (*data.Messages, int, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.cacheEnabled {
		return m.cache.Search(kind, query, start, limit)
	}
	return m.base.Search(kind, query, start, limit)
}

func (m *ManagedStorage) Count() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.cacheEnabled {
		return m.cache.Count()
	}
	return m.base.Count()
}

func (m *ManagedStorage) DeleteOne(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.base.DeleteOne(id); err != nil {
		return err
	}
	if m.cacheEnabled {
		if err := m.cache.DeleteOne(id); err != nil {
			log.Printf("ManagedStorage cache delete failed for message %s: %s", id, err)
		}
	}
	return nil
}

func (m *ManagedStorage) DeleteAll() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.base.DeleteAll(); err != nil {
		return err
	}
	if m.cacheEnabled {
		if err := m.cache.DeleteAll(); err != nil {
			log.Printf("ManagedStorage cache delete-all failed: %s", err)
		}
	}
	return nil
}

func (m *ManagedStorage) Load(id string) (*data.Message, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.cacheEnabled {
		if msg, err := m.cache.Load(id); err == nil && msg != nil {
			return msg, nil
		}
	}
	return m.base.Load(id)
}

func (m *ManagedStorage) SetRetentionDays(days int) {
	if days <= 0 {
		days = 1
	}
	m.mu.Lock()
	m.retentionDays = days
	m.lastRetentionSweep = time.Time{}
	m.mu.Unlock()
}

func (m *ManagedStorage) RetentionDays() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.retentionDays
}

func (m *ManagedStorage) ApplyRetention() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.retentionDays <= 0 {
		return nil
	}
	now := time.Now()
	if !m.lastRetentionSweep.IsZero() && now.Sub(m.lastRetentionSweep) < m.retentionSweepInterval {
		return nil
	}

	var total int
	var messageSource storage.Storage
	if m.cacheEnabled {
		total = m.cache.Count()
		messageSource = m.cache
	} else {
		total = m.base.Count()
		messageSource = m.base
	}
	if total == 0 {
		if m.cacheEnabled {
			_ = m.cache.DeleteAll()
		}
		m.lastRetentionSweep = now
		return nil
	}

	cutoff := now.AddDate(0, 0, -m.retentionDays)
	messages, err := messageSource.List(0, total)
	if err != nil {
		return err
	}

	for _, msg := range *messages {
		if msg.Created.Before(cutoff) {
			if err := m.base.DeleteOne(string(msg.ID)); err != nil {
				return err
			}
			if m.cacheEnabled {
				if err := m.cache.DeleteOne(string(msg.ID)); err != nil {
					log.Printf("ManagedStorage cache retention delete failed for %s: %s", msg.ID, err)
				}
			}
		}
	}
	m.lastRetentionSweep = now
	return nil
}

func (m *ManagedStorage) rebuildCache() error {
	if m.cache == nil {
		return nil
	}
	if err := m.cache.DeleteAll(); err != nil {
		return err
	}

	total := m.base.Count()
	if total == 0 {
		return nil
	}
	messages, err := m.base.List(0, total)
	if err != nil {
		return err
	}
	for _, msg := range *messages {
		messageCopy := msg
		if _, err := m.cache.Store(&messageCopy); err != nil {
			return err
		}
	}
	return nil
}
