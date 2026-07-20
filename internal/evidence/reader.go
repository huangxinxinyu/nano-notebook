package evidence

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/huangxinxinyu/nano-notebook/internal/normalize"
	"github.com/huangxinxinyu/nano-notebook/internal/source"
	"github.com/jackc/pgx/v5"
)

var (
	ErrSourceNotFound      = errors.New("Source evidence not found")
	ErrSourceNotReady      = errors.New("Source evidence not ready")
	ErrEvidenceUnavailable = errors.New("active Source evidence unavailable")
	ErrViewerUnsupported   = errors.New("Source has no inline viewer asset")
)

type ViewerAsset struct {
	Format        source.Format
	ByteSize      int64
	ContentSHA256 string
	ObjectKey     string
}

type SourceView struct {
	ID         string        `json:"id"`
	NotebookID string        `json:"notebook_id"`
	Title      string        `json:"title"`
	Format     source.Format `json:"format"`
	Revision   RevisionView  `json:"revision"`
}

type RevisionView struct {
	ID                 string       `json:"id"`
	RevisionNo         int          `json:"revision_no"`
	ExtractionConfigID string       `json:"extraction_config_id"`
	Coverage           CoverageView `json:"coverage"`
	Units              []UnitView   `json:"units"`
}

type CoverageView struct {
	Status     string    `json:"status"`
	TotalRunes int       `json:"total_runes"`
	Gaps       []GapView `json:"gaps"`
}

type GapView struct {
	Ordinal    int                         `json:"ordinal"`
	StartRune  *int                        `json:"start_rune,omitempty"`
	EndRune    *int                        `json:"end_rune,omitempty"`
	Reason     string                      `json:"reason"`
	Impact     string                      `json:"impact"`
	Coordinate *normalize.SourceCoordinate `json:"coordinate,omitempty"`
}

type UnitView struct {
	ID           string                      `json:"id"`
	Ordinal      int                         `json:"ordinal"`
	Kind         string                      `json:"kind"`
	Text         string                      `json:"text"`
	StartRune    int                         `json:"start_rune"`
	EndRune      int                         `json:"end_rune"`
	HeadingLevel *int                        `json:"heading_level,omitempty"`
	Coordinate   *normalize.SourceCoordinate `json:"coordinate,omitempty"`
}

type readerDB interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

type Reader struct {
	db readerDB
}

func NewReader(db readerDB) *Reader {
	return &Reader{db: db}
}

func (r *Reader) ViewerAsset(ctx context.Context, sourceID string) (ViewerAsset, error) {
	if r == nil || r.db == nil || sourceID == "" {
		return ViewerAsset{}, ErrSourceNotFound
	}
	var asset ViewerAsset
	var state source.State
	err := r.db.QueryRow(ctx, `
		select s.format,s.byte_size,s.content_sha256,s.original_object_key,s.state
		from source_sources s
		where s.id=$1 and exists (
			select 1 from source_evidence_revisions r where r.source_id=s.id and r.status='active'
		)
	`, sourceID).Scan(&asset.Format, &asset.ByteSize, &asset.ContentSHA256, &asset.ObjectKey, &state)
	if errors.Is(err, pgx.ErrNoRows) {
		return ViewerAsset{}, ErrSourceNotFound
	}
	if err != nil {
		return ViewerAsset{}, err
	}
	if state != source.StateReady {
		return ViewerAsset{}, ErrSourceNotReady
	}
	if asset.Format != source.FormatPNG && asset.Format != source.FormatJPEG && asset.Format != source.FormatWebP {
		return ViewerAsset{}, ErrViewerUnsupported
	}
	if asset.ByteSize < 1 || asset.ByteSize > 100*1024*1024 || len(asset.ContentSHA256) != 64 || asset.ObjectKey == "" {
		return ViewerAsset{}, ErrEvidenceUnavailable
	}
	return asset, nil
}

func (r *Reader) SourceView(ctx context.Context, sourceID string) (SourceView, error) {
	if r == nil || r.db == nil || sourceID == "" {
		return SourceView{}, ErrSourceNotFound
	}
	var view SourceView
	var state source.State
	err := r.db.QueryRow(ctx, `
		select id, notebook_id, title, format, state
		from source_sources where id=$1
	`, sourceID).Scan(&view.ID, &view.NotebookID, &view.Title, &view.Format, &state)
	if errors.Is(err, pgx.ErrNoRows) {
		return SourceView{}, ErrSourceNotFound
	}
	if err != nil {
		return SourceView{}, err
	}
	if state != source.StateReady {
		return SourceView{}, ErrSourceNotReady
	}
	err = r.db.QueryRow(ctx, `
		select r.id, r.revision_no, r.extraction_config_id, c.status, c.total_runes
		from source_evidence_revisions r
		join source_evidence_coverage c on c.revision_id=r.id
		where r.source_id=$1 and r.status='active'
	`, sourceID).Scan(
		&view.Revision.ID, &view.Revision.RevisionNo, &view.Revision.ExtractionConfigID,
		&view.Revision.Coverage.Status, &view.Revision.Coverage.TotalRunes,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return SourceView{}, ErrEvidenceUnavailable
	}
	if err != nil {
		return SourceView{}, err
	}
	view.Revision.Coverage.Gaps = make([]GapView, 0)
	rows, err := r.db.Query(ctx, `
		select ordinal, start_rune, end_rune, reason, impact, coordinate_json
		from source_evidence_coverage_gaps where revision_id=$1 order by ordinal
	`, view.Revision.ID)
	if err != nil {
		return SourceView{}, err
	}
	for rows.Next() {
		var gap GapView
		var coordinateJSON []byte
		if err := rows.Scan(&gap.Ordinal, &gap.StartRune, &gap.EndRune, &gap.Reason, &gap.Impact, &coordinateJSON); err != nil {
			rows.Close()
			return SourceView{}, err
		}
		if len(coordinateJSON) > 0 {
			var coordinate normalize.SourceCoordinate
			if err := json.Unmarshal(coordinateJSON, &coordinate); err != nil {
				rows.Close()
				return SourceView{}, err
			}
			gap.Coordinate = &coordinate
		}
		view.Revision.Coverage.Gaps = append(view.Revision.Coverage.Gaps, gap)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return SourceView{}, err
	}
	rows.Close()

	view.Revision.Units = make([]UnitView, 0)
	rows, err = r.db.Query(ctx, `
		select id, ordinal, kind, text_content, start_rune, end_rune, heading_level, coordinate_json
		from source_evidence_units where revision_id=$1 order by ordinal
	`, view.Revision.ID)
	if err != nil {
		return SourceView{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var unit UnitView
		var coordinateJSON []byte
		if err := rows.Scan(&unit.ID, &unit.Ordinal, &unit.Kind, &unit.Text, &unit.StartRune, &unit.EndRune, &unit.HeadingLevel, &coordinateJSON); err != nil {
			return SourceView{}, err
		}
		if len(coordinateJSON) > 0 {
			var coordinate normalize.SourceCoordinate
			if err := json.Unmarshal(coordinateJSON, &coordinate); err != nil {
				return SourceView{}, err
			}
			unit.Coordinate = &coordinate
		}
		view.Revision.Units = append(view.Revision.Units, unit)
	}
	if err := rows.Err(); err != nil {
		return SourceView{}, err
	}
	if len(view.Revision.Units) == 0 {
		return SourceView{}, ErrEvidenceUnavailable
	}
	return view, nil
}
