package m365

import "sync"

// Secrets keeps one secret: the stored sign-in.
type Secrets interface {
	Get() ([]byte, error) // ErrNotConnected when there is none
	Put([]byte) error
	Delete() error
}

// Memory keeps the secret in memory (tests).
type Memory struct {
	mu sync.Mutex
	b  []byte
}

func (m *Memory) Get() ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.b == nil {
		return nil, ErrNotConnected
	}
	return append([]byte(nil), m.b...), nil
}

func (m *Memory) Put(b []byte) error {
	m.mu.Lock()
	m.b = append([]byte(nil), b...)
	m.mu.Unlock()
	return nil
}

func (m *Memory) Delete() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.b == nil {
		return ErrNotConnected
	}
	m.b = nil
	return nil
}
