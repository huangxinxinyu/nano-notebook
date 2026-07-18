package objectstore

import (
	"context"
	"errors"
	"time"
)

var (
	ErrNotFound       = errors.New("object not found")
	ErrObjectTooLarge = errors.New("object exceeds read limit")
)

type ObjectInfo struct {
	Key        string
	Size       int64
	ModifiedAt time.Time
}

type Store interface {
	Put(context.Context, string, []byte) error
	Get(context.Context, string, int64) ([]byte, error)
	Stat(context.Context, string) (ObjectInfo, error)
	Delete(context.Context, string) error
	List(context.Context, string, string, int) ([]ObjectInfo, error)
}
