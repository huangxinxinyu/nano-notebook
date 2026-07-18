package objectstore

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"
)

type MemoryStore struct {
	mu      sync.RWMutex
	objects map[string]memoryObject
}

type memoryObject struct {
	payload    []byte
	modifiedAt time.Time
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{objects: make(map[string]memoryObject)}
}

func (s *MemoryStore) Put(ctx context.Context, key string, payload []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if strings.TrimSpace(key) == "" || len(payload) == 0 {
		return errors.New("object key and payload are required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.objects[key] = memoryObject{payload: append([]byte(nil), payload...), modifiedAt: time.Now().UTC()}
	return nil
}

func (s *MemoryStore) Get(ctx context.Context, key string, maxBytes int64) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if maxBytes < 1 {
		return nil, errors.New("object read limit must be positive")
	}
	s.mu.RLock()
	object, ok := s.objects[key]
	s.mu.RUnlock()
	if !ok {
		return nil, ErrNotFound
	}
	if int64(len(object.payload)) > maxBytes {
		return nil, ErrObjectTooLarge
	}
	return append([]byte(nil), object.payload...), nil
}

func (s *MemoryStore) Stat(ctx context.Context, key string) (ObjectInfo, error) {
	if err := ctx.Err(); err != nil {
		return ObjectInfo{}, err
	}
	s.mu.RLock()
	object, ok := s.objects[key]
	s.mu.RUnlock()
	if !ok {
		return ObjectInfo{}, ErrNotFound
	}
	return ObjectInfo{Key: key, Size: int64(len(object.payload)), ModifiedAt: object.modifiedAt}, nil
}

func (s *MemoryStore) Delete(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	delete(s.objects, key)
	s.mu.Unlock()
	return nil
}

func (s *MemoryStore) List(ctx context.Context, prefix, after string, limit int) ([]ObjectInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if limit < 1 {
		return nil, errors.New("object list limit must be positive")
	}
	s.mu.RLock()
	items := make([]ObjectInfo, 0, len(s.objects))
	for key, object := range s.objects {
		if strings.HasPrefix(key, prefix) && key > after {
			items = append(items, ObjectInfo{Key: key, Size: int64(len(object.payload)), ModifiedAt: object.modifiedAt})
		}
	}
	s.mu.RUnlock()
	sort.Slice(items, func(i, j int) bool { return items[i].Key < items[j].Key })
	if len(items) > limit {
		items = items[:limit]
	}
	return items, nil
}

func (s *MemoryStore) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.objects)
}
