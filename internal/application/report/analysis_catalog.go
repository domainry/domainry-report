package report

import (
	"context"
	"crypto/hmac"
	"encoding/base64"
	"encoding/json"
	"sort"
	"strings"

	reportsdk "github.com/domainry/domainry-report-sdk"
	model "github.com/domainry/domainry-report-sdk/model"
	analysis "github.com/domainry/domainry-report/internal/domain/report/service/analysis"
)

func (s *QueryService) analysisDatasets(ctx context.Context, authority model.ReportAuthority) (model.ReportSubject, []model.AnalysisDataset, error) {
	return s.analysisDatasetsForAction(ctx, authority, reportsdk.ActionReportQueryExecute)
}

func (s *QueryService) analysisDatasetsForAction(ctx context.Context, authority model.ReportAuthority, action string) (model.ReportSubject, []model.AnalysisDataset, error) {
	hosts, err := s.analysisSources()
	if err != nil {
		return model.ReportSubject{}, nil, err
	}
	subject, err := s.resolveSubject(ctx, authority)
	if err != nil {
		return model.ReportSubject{}, nil, err
	}
	if !subject.HasPermission(action) {
		return model.ReportSubject{}, nil, reportError(403, "backend.permission.denied", nil)
	}
	datasets := []model.AnalysisDataset{}
	for _, host := range hosts {
		items, err := host.source.ReportAnalysisSources(ctx, subject)
		if err != nil {
			return model.ReportSubject{}, nil, normalizeHostError(err, "backend.report.analysis.catalog_failed")
		}
		for _, item := range items {
			if item.Kind != host.kind {
				return model.ReportSubject{}, nil, reportError(500, "backend.report.analysis.dataset_invalid", nil)
			}
		}
		datasets = append(datasets, items...)
	}
	if len(datasets) > 4096 {
		return model.ReportSubject{}, nil, reportError(500, "backend.report.analysis.catalog_limit_exceeded", nil)
	}
	visible := make([]model.AnalysisDataset, 0, len(datasets))
	keys := map[string]bool{}
	for _, dataset := range datasets {
		if err := ctx.Err(); err != nil {
			return model.ReportSubject{}, nil, err
		}
		// Discovery does not grant process source access. Unlike a published
		// Report definition, an arbitrary dataset has no owner-declared grant.
		if dataset.Kind == "business_object" && !subject.HasPermission(dataset.Key+".read") {
			continue
		}
		if keys[dataset.Key] || len(dataset.Name) > 256 {
			return model.ReportSubject{}, nil, reportError(500, "backend.report.analysis.dataset_invalid", nil)
		}
		keys[dataset.Key] = true
		if _, err := analysis.Compile(model.AnalysisRequest{DatasetKey: dataset.Key, Measures: []model.AnalysisMeasure{{Key: "source_count", Function: "count"}}}, dataset); err != nil {
			return model.ReportSubject{}, nil, reportError(500, "backend.report.analysis.dataset_invalid", err)
		}
		dataset.Columns = append([]model.AnalysisColumn{}, dataset.Columns...)
		dataset.References = append([]model.AnalysisReference{}, dataset.References...)
		sort.Slice(dataset.Columns, func(i, j int) bool { return dataset.Columns[i].Key < dataset.Columns[j].Key })
		visible = append(visible, dataset)
	}
	sort.Slice(visible, func(i, j int) bool { return visible[i].Key < visible[j].Key })
	return subject, visible, nil
}

func (s *QueryService) AnalysisCatalog(ctx context.Context, request model.AnalysisCatalogRequest, authority model.ReportAuthority) (model.AnalysisCatalog, error) {
	size := request.Page.PageSize
	if size == 0 {
		size = 20
	}
	if size < 1 || size > 50 || len(request.Page.Cursor) > 2048 || len(request.DatasetKey) > 128 {
		return model.AnalysisCatalog{}, reportError(400, "backend.report.analysis.catalog_request_invalid", nil)
	}
	subject, datasets, err := s.analysisDatasets(ctx, authority)
	if err != nil {
		return model.AnalysisCatalog{}, err
	}
	key := strings.TrimSpace(request.DatasetKey)
	if key != "" {
		filtered := []model.AnalysisDataset{}
		for _, dataset := range datasets {
			if dataset.Key == key {
				filtered = append(filtered, dataset)
			}
		}
		datasets = filtered
	}
	fingerprint := canonicalHash(struct {
		Datasets   []model.AnalysisDataset
		Scope, Key string
	}{datasets, reportSnapshotAccessScopeHash(subject, nil), key})
	position := 0
	if request.Page.Cursor != "" {
		content, err := base64.RawURLEncoding.DecodeString(request.Page.Cursor)
		var cursor catalogCursor
		if err != nil || json.Unmarshal(content, &cursor) != nil || cursor.Fingerprint != fingerprint || cursor.Position < 1 || cursor.Position >= len(datasets) || !hmac.Equal([]byte(cursor.Proof), []byte(s.analysisCatalogProof(cursor))) {
			return model.AnalysisCatalog{}, reportError(400, "backend.report.cursor_invalid", nil)
		}
		position = cursor.Position
	}
	end := position + size
	if end > len(datasets) {
		end = len(datasets)
	}
	out := model.AnalysisCatalog{Datasets: append([]model.AnalysisDataset{}, datasets[position:end]...)}
	if end < len(datasets) {
		cursor := catalogCursor{Fingerprint: fingerprint, Position: end}
		cursor.Proof = s.analysisCatalogProof(cursor)
		content, _ := json.Marshal(cursor)
		out.NextCursor, out.Truncated = base64.RawURLEncoding.EncodeToString(content), true
	}
	out.ReadProof = s.analysisCatalogReadProof(request, out, datasets, subject)
	return out, nil
}

func (s *QueryService) analysisCatalogProof(cursor catalogCursor) string {
	cursor.Proof = ""
	return s.queryProof("report-analysis-catalog-v1", cursor)
}
