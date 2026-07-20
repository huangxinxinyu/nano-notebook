package app_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/huangxinxinyu/nano-notebook/internal/objectstore"
	"github.com/huangxinxinyu/nano-notebook/internal/qdrantstore"
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
	const artifactKey = "sources/src_purge/evidence/evr_purge/normalized.json"
	const viewerKey = "sources/src_purge/evidence/evr_purge/viewer/page-000001.png"
	if err := objects.Put(context.Background(), objectKey, []byte("purge me")); err != nil {
		t.Fatal(err)
	}
	if err := objects.Put(context.Background(), artifactKey, []byte("normalized purge me")); err != nil {
		t.Fatal(err)
	}
	if err := objects.Put(context.Background(), viewerKey, []byte("rendered purge me")); err != nil {
		t.Fatal(err)
	}
	if _, err := api.db.Pool().Exec(context.Background(), `
		insert into source_evidence_revisions(
			id,source_id,notebook_id,revision_no,extraction_config_id,artifact_schema_version,
			artifact_object_key,artifact_sha256,status,activated_at
		) values ('evr_purge','src_purge',$1,1,'extract-v1','nano.normalized-source.v1',$2,$3,'active',now())
	`, notebookID, artifactKey, strings.Repeat("a", 64)); err != nil {
		t.Fatal(err)
	}
	if _, err := api.db.Pool().Exec(context.Background(), `
		insert into source_viewer_artifacts(
			revision_id,source_id,notebook_id,ordinal,width,height,media_type,byte_size,content_sha256,filename,object_key,render_config_id
		) values('evr_purge','src_purge',$1,1,1,1,'image/png',17,$2,'page-000001.png',$3,'pdfium-v1')
	`, notebookID, strings.Repeat("d", 64), viewerKey); err != nil {
		t.Fatal(err)
	}
	if _, err := api.db.Pool().Exec(context.Background(), `
		insert into retrieval_index_versions(
			id,config_json,config_sha256,status,promoted_by_eval_run_id,promoted_at
		) values ('riv_purge','{}'::jsonb,$1,'active','eval_purge',now())
	`, strings.Repeat("b", 64)); err != nil {
		t.Fatal(err)
	}
	if _, err := api.db.Pool().Exec(context.Background(), `
		insert into retrieval_source_index_builds(
			revision_id,index_version_id,source_id,notebook_id,expected_points,projection_sha256,status,verified_at
		) values ('evr_purge','riv_purge','src_purge',$1,1,$2,'verified',now())
	`, notebookID, strings.Repeat("c", 64)); err != nil {
		t.Fatal(err)
	}
	err := api.db.WithRequestPrincipal(context.Background(), ownerID, func(tx pgx.Tx) error {
		_, err := source.NewStore(tx).Remove(context.Background(), "src_purge", "srcpurge_processor")
		return err
	})
	if err != nil {
		t.Fatal(err)
	}

	projections := &recordingProjectionPurger{}
	processor := sourcepurge.NewProcessorWithProjectionPurger(api.db.Pool(), objects, projections, 30*time.Second)
	processed, err := processor.RunOnce(context.Background())
	if err != nil || !processed {
		t.Fatalf("RunOnce processed=%v err=%v", processed, err)
	}
	if objects.Len() != 0 || len(projections.scopes) != 1 || projections.scopes[0].NotebookID != notebookID ||
		projections.scopes[0].IndexVersionID != "riv_purge" || len(projections.scopes[0].Evidence) != 1 ||
		projections.scopes[0].Evidence[0].RevisionID != "evr_purge" {
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

type recordingProjectionPurger struct {
	scopes []qdrantstore.Scope
}

func (p *recordingProjectionPurger) DeleteScope(_ context.Context, scope qdrantstore.Scope) error {
	p.scopes = append(p.scopes, scope)
	return nil
}

func TestSourcePurgeProcessorMaterializesExpiredUploadIntent(t *testing.T) {
	api := newTestAPI(t)
	owner := api.register(t, "source-expired-upload@example.com")
	notebookID := createSourceTestNotebook(t, api, owner, "source-expired-upload")
	ownerID := sourceTestUserID(t, api, "source-expired-upload@example.com")
	const objectKey = "source-upload-intents/upl_expired/payload"
	err := api.db.WithRequestPrincipal(context.Background(), ownerID, func(tx pgx.Tx) error {
		_, _, err := source.NewStore(tx).CreateUploadIntent(context.Background(), source.CreateUploadIntentCommand{
			ID: "upl_expired", SourceID: "src_expired", NotebookID: notebookID,
			IdempotencyKey: "expired-upload", RequestHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			Title: "expired.txt", Format: source.FormatTXT, MediaType: "text/plain", ByteSize: 7,
			ContentSHA256: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
			ObjectKey:     objectKey, ExpiresAt: time.Now().UTC().Add(time.Minute),
		})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := api.db.Pool().Exec(context.Background(), `
		update source_upload_intents
		set created_at=now()-interval '2 hours', expires_at=now()-interval '1 hour'
		where id='upl_expired'
	`); err != nil {
		t.Fatal(err)
	}
	objects := objectstore.NewMemoryStore()
	if err := objects.Put(context.Background(), objectKey, []byte("expired")); err != nil {
		t.Fatal(err)
	}
	processor := sourcepurge.NewProcessor(api.db.Pool(), objects, 30*time.Second)
	processed, err := processor.RunOnce(context.Background())
	if err != nil || !processed {
		t.Fatalf("RunOnce processed=%v err=%v", processed, err)
	}
	var intentState source.UploadIntentState
	var purgeState string
	if err := api.db.Pool().QueryRow(context.Background(), `select state from source_upload_intents where id='upl_expired'`).Scan(&intentState); err != nil {
		t.Fatal(err)
	}
	if err := api.db.Pool().QueryRow(context.Background(), `select state from source_purge_jobs where source_id='src_expired'`).Scan(&purgeState); err != nil {
		t.Fatal(err)
	}
	if intentState != source.UploadIntentExpired || purgeState != "succeeded" || objects.Len() != 0 {
		t.Fatalf("expired intent=%q purge=%q objects=%d", intentState, purgeState, objects.Len())
	}
}
