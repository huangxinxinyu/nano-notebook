package app_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/huangxinxinyu/nano-notebook/internal/app"
	"github.com/huangxinxinyu/nano-notebook/internal/objectstore"
)

func TestReadySourceViewerReturnsAuthoritativeUnitsAndCoverageWithoutCustody(t *testing.T) {
	api := newTestAPI(t)
	owner := api.register(t, "source-view@example.com")
	intruder := api.register(t, "source-view-intruder@example.com")
	notebookID := createSourceTestNotebook(t, api, owner, "source-view")
	ownerID := sourceTestUserID(t, api, "source-view@example.com")
	seedSourceProcessingJob(t, api, ownerID, notebookID, "src_view", "srcjob_view", "4")
	if _, err := api.db.Pool().Exec(context.Background(), `
		update source_sources set state='ready', format='pdf', media_type='application/pdf' where id='src_view'
	`); err != nil {
		t.Fatal(err)
	}
	if _, err := api.db.Pool().Exec(context.Background(), `
		insert into source_evidence_revisions(
			id, source_id, notebook_id, revision_no, extraction_config_id, artifact_schema_version,
			artifact_object_key, artifact_sha256, status, activated_at
		) values (
			'evr_view','src_view',$1,1,'extract-text-v1','nano.normalized-source.v1',
			'sources/src_view/evidence/evr_view/normalized.json',$2,'active',now()
		)
	`, notebookID, strings.Repeat("4", 64)); err != nil {
		t.Fatal(err)
	}
	if _, err := api.db.Pool().Exec(context.Background(), `
		insert into source_evidence_coverage(revision_id,status,total_runes) values ('evr_view','partial',20)
	`); err != nil {
		t.Fatal(err)
	}
	if _, err := api.db.Pool().Exec(context.Background(), `
		insert into source_evidence_coverage_gaps(
			revision_id,ordinal,start_rune,end_rune,reason,impact,coordinate_json
		) values (
			'evr_view',0,null,null,'decorative_visual_skipped','non_primary',
			'{"kind":"pdf_region","page":1,"x":300,"y":500,"width":80,"height":60}'::jsonb
		)
	`); err != nil {
		t.Fatal(err)
	}
	if _, err := api.db.Pool().Exec(context.Background(), `
		insert into source_evidence_units(
			id,revision_id,source_id,notebook_id,ordinal,kind,text_content,start_rune,end_rune,coordinate_json
		) values
			('unit_view_1','evr_view','src_view',$1,0,'paragraph','First evidence.',0,10,
			 '{"kind":"pdf_region","page":2,"x":72,"y":640,"width":120,"height":12}'::jsonb),
			('unit_view_2','evr_view','src_view',$1,1,'paragraph','Second.',12,19,null)
	`, notebookID); err != nil {
		t.Fatal(err)
	}
	pagePayload := mustDecodeBase64("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAusB9Wl2n0YAAAAASUVORK5CYII=")
	pageDigest := sha256.Sum256(pagePayload)
	pageKey := "sources/src_view/evidence/evr_view/viewer/page-000001.png"
	if _, err := api.db.Pool().Exec(context.Background(), `
		insert into source_viewer_artifacts(
			revision_id,source_id,notebook_id,ordinal,width,height,media_type,byte_size,content_sha256,filename,object_key,render_config_id
		) values('evr_view','src_view',$1,1,1,1,'image/png',$2,$3,'page-000001.png',$4,'pdfium-v1')
	`, notebookID, len(pagePayload), fmt.Sprintf("%x", pageDigest), pageKey); err != nil {
		t.Fatal(err)
	}
	objects := objectstore.NewMemoryStore()
	if err := objects.Put(context.Background(), pageKey, pagePayload); err != nil {
		t.Fatal(err)
	}
	api.server = app.NewServer(app.Config{CookieSecure: false, SourceSnapshots: objects}, api.db)
	api.handler = api.server.Handler()

	response := getSourceView(t, api, owner, "src_view")
	if response.Code != http.StatusOK {
		t.Fatalf("viewer status=%d body=%s", response.Code, response.Body.String())
	}
	var decoded struct {
		Source struct {
			ID       string `json:"id"`
			Title    string `json:"title"`
			Format   string `json:"format"`
			Revision struct {
				ID     string `json:"id"`
				Viewer *struct {
					Kind      string `json:"kind"`
					PageCount int    `json:"page_count"`
				} `json:"viewer"`
				Coverage struct {
					Status string `json:"status"`
					Gaps   []struct {
						Reason     string `json:"reason"`
						Impact     string `json:"impact"`
						Coordinate *struct {
							Kind string `json:"kind"`
							Page int    `json:"page"`
						} `json:"coordinate"`
					} `json:"gaps"`
				} `json:"coverage"`
				Units []struct {
					ID         string `json:"id"`
					Text       string `json:"text"`
					Coordinate *struct {
						Kind string `json:"kind"`
						Page int    `json:"page"`
					} `json:"coordinate"`
				} `json:"units"`
			} `json:"revision"`
		} `json:"source"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Source.ID != "src_view" || decoded.Source.Revision.ID != "evr_view" ||
		decoded.Source.Revision.Coverage.Status != "partial" || len(decoded.Source.Revision.Coverage.Gaps) != 1 ||
		decoded.Source.Revision.Coverage.Gaps[0].Reason != "decorative_visual_skipped" ||
		decoded.Source.Revision.Coverage.Gaps[0].Impact != "non_primary" ||
		decoded.Source.Revision.Coverage.Gaps[0].Coordinate == nil ||
		decoded.Source.Revision.Coverage.Gaps[0].Coordinate.Page != 1 || len(decoded.Source.Revision.Units) != 2 ||
		decoded.Source.Revision.Units[0].ID != "unit_view_1" || decoded.Source.Revision.Units[0].Coordinate == nil ||
		decoded.Source.Revision.Units[0].Coordinate.Kind != "pdf_region" || decoded.Source.Revision.Units[0].Coordinate.Page != 2 ||
		decoded.Source.Revision.Units[1].Coordinate != nil || decoded.Source.Revision.Viewer == nil ||
		decoded.Source.Revision.Viewer.Kind != "pages" || decoded.Source.Revision.Viewer.PageCount != 1 {
		t.Fatalf("viewer response=%+v", decoded)
	}
	pageResponse := api.getWithCookie(t, "/api/v1/sources/src_view/viewer-asset?ordinal=1", owner)
	if pageResponse.Code != http.StatusOK || pageResponse.Body.String() != string(pagePayload) ||
		pageResponse.Header().Get("Content-Disposition") != `inline; filename="page-000001.png"` {
		t.Fatalf("PDF page status=%d headers=%v body=%x", pageResponse.Code, pageResponse.Header(), pageResponse.Body.Bytes())
	}
	for _, forbidden := range []string{"original_object_key", "artifact_object_key", "artifact_sha256", "content_sha256", "download_url"} {
		if strings.Contains(response.Body.String(), forbidden) {
			t.Fatalf("viewer exposed %q: %s", forbidden, response.Body.String())
		}
	}

	for _, cookie := range []*http.Cookie{intruder, nil} {
		denied := getSourceView(t, api, cookie, "src_view")
		want := http.StatusNotFound
		if cookie == nil {
			want = http.StatusUnauthorized
		}
		if denied.Code != want {
			t.Fatalf("denied status=%d want=%d body=%s", denied.Code, want, denied.Body.String())
		}
	}
}

func TestImageViewerAssetStreamsOnlyAuthorizedReadyInlineContent(t *testing.T) {
	api := newTestAPI(t)
	owner := api.register(t, "image-view@example.com")
	intruder := api.register(t, "image-view-intruder@example.com")
	notebookID := createSourceTestNotebook(t, api, owner, "image-view")
	ownerID := sourceTestUserID(t, api, "image-view@example.com")
	payload := mustDecodeBase64("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAusB9Wl2n0YAAAAASUVORK5CYII=")
	digest := sha256.Sum256(payload)
	objectKey := fmt.Sprintf("sources/src_image_view/original/%x", digest)
	seedSourceProcessingJob(t, api, ownerID, notebookID, "src_image_view", "srcjob_image_view", "5")
	if _, err := api.db.Pool().Exec(context.Background(), `
		update source_sources set state='ready', title='diagram.png', format='png', media_type='image/png',
			byte_size=$2, content_sha256=$3, original_object_key=$4 where id=$1
	`, "src_image_view", len(payload), fmt.Sprintf("%x", digest), objectKey); err != nil {
		t.Fatal(err)
	}
	if _, err := api.db.Pool().Exec(context.Background(), `
		insert into source_evidence_revisions(id,source_id,notebook_id,revision_no,extraction_config_id,artifact_schema_version,artifact_object_key,artifact_sha256,status,activated_at)
		values('evr_image_view',$1,$2,1,'extract-image-v1','nano.normalized-source.v1','normalized/image.json',$3,'active',now())
	`, "src_image_view", notebookID, fmt.Sprintf("%x", digest)); err != nil {
		t.Fatal(err)
	}
	if _, err := api.db.Pool().Exec(context.Background(), `
		insert into source_evidence_coverage(revision_id,status,total_runes) values('evr_image_view','complete',7)
	`); err != nil {
		t.Fatal(err)
	}
	if _, err := api.db.Pool().Exec(context.Background(), `
		insert into source_evidence_units(id,revision_id,source_id,notebook_id,ordinal,kind,text_content,start_rune,end_rune,coordinate_json)
		values('unit_image_view','evr_image_view',$1,$2,0,'paragraph','Diagram',0,7,'{"kind":"image_region","x":0,"y":0,"width":1,"height":1}'::jsonb)
	`, "src_image_view", notebookID); err != nil {
		t.Fatal(err)
	}
	objects := objectstore.NewMemoryStore()
	if err := objects.Put(context.Background(), objectKey, payload); err != nil {
		t.Fatal(err)
	}
	api.server = app.NewServer(app.Config{CookieSecure: false, SourceSnapshots: objects}, api.db)
	api.handler = api.server.Handler()

	response := api.getWithCookie(t, "/api/v1/sources/src_image_view/viewer-asset", owner)
	if response.Code != http.StatusOK || response.Body.String() != string(payload) || response.Header().Get("Content-Type") != "image/png" ||
		response.Header().Get("Content-Disposition") != `inline; filename="source-image"` || response.Header().Get("Cache-Control") != "private, no-store" ||
		response.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("viewer asset status=%d headers=%v body=%x", response.Code, response.Header(), response.Body.Bytes())
	}
	denied := api.getWithCookie(t, "/api/v1/sources/src_image_view/viewer-asset", intruder)
	if denied.Code != http.StatusNotFound || strings.Contains(denied.Body.String(), objectKey) {
		t.Fatalf("intruder viewer asset=%d %s", denied.Code, denied.Body.String())
	}
}

func getSourceView(t *testing.T, api *testAPI, cookie *http.Cookie, sourceID string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/sources/"+sourceID, nil)
	if cookie != nil {
		request.AddCookie(cookie)
	}
	response := httptest.NewRecorder()
	api.handler.ServeHTTP(response, request)
	return response
}
