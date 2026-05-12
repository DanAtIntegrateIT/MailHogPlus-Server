package config

import (
	"sync"
	"time"

	"github.com/mailhog/data"
	"github.com/mailhog/storage"
)

// ManagedStorage wraps a storage backend and applies retention policy.
type ManagedStorage struct {
	base          storage.Storage
	mu            sync.RWMutex
	retentionDays int
}

func NewManagedStorage(base storage.Storage, retentionDays int) *ManagedStorage {
	if retentionDays <= 0 {
		retentionDays = 10
	}
	return &ManagedStorage{
		base:          base,
		retentionDays: retentionDays,
	}
}

func (m *ManagedStorage) Store(msg *data.Message) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.base.Store(msg)
}

func (m *ManagedStorage) List(start, limit int) (*data.Messages, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.base.List(start, limit)
}

func (m *ManagedStorage) Search(kind, query string, start, limit int) (*data.Messages, int, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.base.Search(kind, query, start, limit)
}

func (m *ManagedStorage) Count() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.base.Count()
}

func (m *ManagedStorage) DeleteOne(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.base.DeleteOne(id)
}

func (m *ManagedStorage) DeleteAll() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.base.DeleteAll()
}

func (m *ManagedStorage) Load(id string) (*data.Message, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.base.Load(id)
}

func (m *ManagedStorage) SetRetentionDays(days int) {
	if days <= 0 {
		days = 1
	}
	m.mu.Lock()
	m.retentionDays = days
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

	total := m.base.Count()
	if total == 0 {
		return nil
	}

	cutoff := time.Now().AddDate(0, 0, -m.retentionDays)
	messages, err := m.base.List(0, total)
	if err != nil {
		return err
	}

	for _, msg := range *messages {
		if msg.Created.Before(cutoff) {
			if err := m.base.DeleteOne(string(msg.ID)); err != nil {
				return err
			}
		}
	}
	return nil
}
