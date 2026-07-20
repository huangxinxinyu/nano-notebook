package app_test

import (
	"context"
	"testing"
	"time"

	"github.com/huangxinxinyu/nano-notebook/internal/objectstore"
	"github.com/huangxinxinyu/nano-notebook/internal/source"
	"github.com/huangxinxinyu/nano-notebook/internal/sourcepurge"
	"github.com/jackc/pgx/v5"
)

func TestSourcePurgeProcessorDeletesCustodyBeforeCompleting(t *testing.T) {
	api := newTestAPI(t)
	owner := api.register(t, "source-purge@example.com")
	notebookID := createSourceTestNotebook(t, api, owner, "source-purge")
	ownerID := sourceTestUserID(t, api, "source-purge@example.com")
	seedSourceProcessingJob(t, api, ownerID, notebookID, "src_purge", "srcjob_purge", "3")

	objects := objectstore.NewMemoryStore()
	const objectKey = "sources/src_purge/original/3333333333333333333333333333333333333333333333333333333333333333"
	if err := objects.Put(context.Background(), objectKey, []byte("purge me")); err != nil {
		t.Fatal(err)
	}
	err := api.db.WithRequestPrincipal(context.Background(), ownerID, func(tx pgx.Tx) error {
		_, err := source.NewStore(tx).Remove(context.Background(), "src_purge", "srcpurge_processor")
		return err
	})
	if err != nil {
		t.Fatal(err)
	}

	processor := sourcepurge.NewProcessor(api.db.Pool(), objects, 30*time.Second)
	processed, err := processor.RunOnce(context.Background())
	if err != nil || !processed {
		t.Fatalf("RunOnce processed=%v err=%v", processed, err)
	}
	if objects.Len() != 0 {
		t.Fatalf("objects after purge = %d", objects.Len())
	}
	var state string
	if err := api.db.Pool().QueryRow(context.Background(), `select state from source_purge_jobs where id='srcpurge_processor'`).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "succeeded" {
		t.Fatalf("purge state = %q, want succeeded", state)
	}
	processed, err = processor.RunOnce(context.Background())
	if err != nil || processed {
		t.Fatalf("empty RunOnce processed=%v err=%v", processed, err)
	}
}
