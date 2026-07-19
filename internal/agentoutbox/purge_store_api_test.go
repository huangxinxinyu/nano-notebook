package agentoutbox_test

import (
	"testing"

	"github.com/huangxinxinyu/nano-notebook/internal/agentoutbox"
)

func TestPurgeStoreRejectsNilPostgresPool(t *testing.T) {
	if _, err := agentoutbox.NewPurgeStore(nil, agentoutbox.PurgeStoreConfig{}); err == nil {
		t.Fatal("NewPurgeStore accepted a nil PostgreSQL pool")
	}
}
