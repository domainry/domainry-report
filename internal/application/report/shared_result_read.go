package report

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	sdk "github.com/domainry/domainry-report-sdk"
	model "github.com/domainry/domainry-report-sdk/model"
	"github.com/domainry/domainry-report-sdk/modulehost"
)

func sameSharedWorkspace(producer, reader model.ReportSubject) error {
	if producer.Principal.WorkspaceID != reader.Principal.WorkspaceID {
		return reportError(403, "backend.permission.denied", nil)
	}
	return nil
}

func (s *QueryService) sharedResultScope(ctx context.Context, definition model.ReportSchema, producer, reader model.ReportSubject) error {
	if err := sameSharedWorkspace(producer, reader); err != nil {
		return err
	}
	owner, ok := s.objectSQL.(modulehost.SharedResultScopeAuthorizer)
	if !ok {
		return reportError(503, "backend.report.shared_read_scope_unavailable", nil)
	}
	if err := owner.AuthorizeSharedReportResultScope(ctx, definition, producer, reader); err != nil {
		return normalizeHostError(err, "backend.report.shared_read_scope_denied")
	}
	return ctx.Err()
}

func (s *QueryService) AuthorizeSharedQueryResultRead(ctx context.Context, in model.ReportQueryResultAuthorization, a, origin model.ReportAuthority) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	reader, err := s.queryEvidenceStateForAction(ctx, in.Query.ReportKey, a, sdk.ActionReportResultsRead)
	if err != nil {
		return err
	}
	producer, err := s.queryEvidenceStateForAction(ctx, in.Query.ReportKey, origin, "")
	if err != nil {
		return err
	}
	query, err := normalizedEvidenceQuery(producer.report, in.Query)
	if err != nil {
		return err
	}
	if producer.readScope == "" {
		return reportError(503, "backend.report.read_scope_unavailable", nil)
	}
	if !validReadProof(in.Result.Source.ReadProof, s.queryResultReadProof(query, in.Result, producer)) {
		return reportError(409, "backend.report.result_stale_or_invalid", nil)
	}
	return s.sharedResultScope(ctx, producer.report, producer.subject, reader.subject)
}

func (s *QueryService) AuthorizeSharedAnalysisResultRead(ctx context.Context, in model.AnalysisResultAuthorization, a, origin model.ReportAuthority) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	raw, err := json.Marshal(in.Result)
	if err != nil || len(raw) > analysisMaximumResultBytes+256 {
		return reportError(400, "backend.report.analysis.result_invalid", err)
	}
	reader, readerErr := s.analysisStateForAction(ctx, in.Request, a, sdk.ActionReportResultsRead)
	if readerErr != nil {
		var failure *sdk.Error
		// A source owner's authorized catalog can omit revoked fields or an
		// entire dataset. Defer only these errors until the producer's proof
		// establishes that this was a valid original specification. Service
		// failures and explicit permission denials keep their classification.
		if !errors.As(readerErr, &failure) || failure.Code != "backend.report.analysis.spec_invalid" && failure.Code != "backend.report.analysis.dataset_not_found" {
			return readerErr
		}
	}
	producer, err := s.analysisStateForAction(ctx, in.Request, origin, "")
	if err != nil {
		return err
	}
	if producer.readScope == "" {
		return reportError(503, "backend.report.read_scope_unavailable", nil)
	}
	if !validReadProof(in.Result.Source.ReadProof, s.analysisResultReadProof(in.Result, producer)) {
		return reportError(409, "backend.report.analysis.result_stale_or_invalid", nil)
	}
	if readerErr != nil {
		return reportError(403, "backend.permission.denied", nil)
	}
	if err := sameSharedWorkspace(producer.subject, reader.subject); err != nil {
		return err
	}
	if producer.table != nil {
		if reader.table == nil || *producer.table != *reader.table {
			return reportError(403, "backend.permission.denied", nil)
		}
		return ctx.Err()
	}
	for _, query := range producer.queries {
		if err := s.sharedResultScope(ctx, query.Report, producer.subject, reader.subject); err != nil {
			return err
		}
	}
	return ctx.Err()
}

func (s *QueryService) AuthorizeSharedCatalogRead(ctx context.Context, in model.ReportCatalogReadAuthorization, a, origin model.ReportAuthority) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if s == nil || s.definitions == nil || len(s.cursorKey) == 0 || len(in.Result.Reports) > model.ReportCatalogMaximumSize {
		return reportError(503, "backend.report.catalog_unavailable", nil)
	}
	reader, err := s.resolveSubject(ctx, a)
	if err != nil {
		return err
	}
	if !reader.HasPermission(sdk.ActionReportResultsRead) {
		return reportError(403, "backend.permission.denied", nil)
	}
	producer, err := s.resolveSubject(ctx, origin)
	if err != nil {
		return err
	}
	if err := sameSharedWorkspace(producer, reader); err != nil {
		return err
	}
	definitions, err := s.definitions.ReportDefinitions(ctx)
	if err != nil {
		return normalizeHostError(err, "backend.report.definition_read_failed")
	}
	definitions = append([]model.ReportSchema{}, definitions...)
	sort.Slice(definitions, func(i, j int) bool { return definitions[i].Key < definitions[j].Key })
	if !validReadProof(in.Result.ReadProof, s.catalogReadProof(in.Request, in.Result, definitions, producer)) {
		return reportError(409, "backend.report.result_stale_or_invalid", nil)
	}
	for _, entry := range in.Result.Reports {
		subject, definition, err := s.resolve(ctx, entry.Key, a, sdk.ActionReportResultsRead)
		if err != nil {
			return err
		}
		if _, err := s.compileAuthorizedObjectSQL(ctx, reportForDataPermissions(definition, reportDataPermissionKeys(definition)), subject); err != nil {
			return err
		}
	}
	return ctx.Err()
}

func (s *QueryService) AuthorizeSharedAnalysisCatalogRead(ctx context.Context, in model.AnalysisCatalogReadAuthorization, a, origin model.ReportAuthority) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if s == nil || len(s.cursorKey) == 0 || len(in.Result.Datasets) > 50 {
		return reportError(503, "backend.report.analysis.unavailable", nil)
	}
	producer, err := s.resolveSubject(ctx, origin)
	if err != nil {
		return err
	}
	// The original metadata proof is checked against the producer's current
	// source list, without requiring its former query operation grant.
	_, original, err := s.analysisDatasetsForAction(ctx, origin, "")
	if err != nil {
		return err
	}
	if key := strings.TrimSpace(in.Request.DatasetKey); key != "" {
		filtered := []model.AnalysisDataset{}
		for _, item := range original {
			if item.Key == key {
				filtered = append(filtered, item)
			}
		}
		original = filtered
	}
	if len(in.Result.Datasets) > 50 || !validReadProof(in.Result.ReadProof, s.analysisCatalogReadProof(in.Request, in.Result, original, producer)) {
		return reportError(409, "backend.report.analysis.result_stale_or_invalid", nil)
	}
	reader, current, err := s.analysisDatasetsForAction(ctx, a, sdk.ActionReportResultsRead)
	if err != nil {
		return err
	}
	if err := sameSharedWorkspace(producer, reader); err != nil {
		return err
	}
	for _, released := range in.Result.Datasets {
		found := false
		for _, candidate := range current {
			found = found || canonicalHash(candidate) == canonicalHash(released)
		}
		if !found {
			return reportError(403, "backend.permission.denied", nil)
		}
	}
	return ctx.Err()
}

var _ sdk.SharedResultReader = (*QueryService)(nil)
