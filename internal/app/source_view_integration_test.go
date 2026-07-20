package app_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
				ID       string `json:"id"`
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
		decoded.Source.Revision.Units[1].Coordinate != nil {
		t.Fatalf("viewer response=%+v", decoded)
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
