package report

import (
	"bytes"
	"context"
	"crypto/hmac"
	"encoding/base64"
	"encoding/json"
	"errors"
	"sort"
	"strings"

	reportsdk "github.com/domainry/domainry-report-sdk"
	reportmodel "github.com/domainry/domainry-report-sdk/model"
)

type catalogCursor struct {
	Fingerprint string `json:"fingerprint"`
	Position    int    `json:"position"`
	Proof       string `json:"proof"`
}

func (s *QueryService) Catalog(ctx context.Context, request reportmodel.ReportCatalogRequest, authority reportmodel.ReportAuthority) (reportmodel.ReportCatalog, error) {
	if s == nil || s.definitions == nil || len(s.cursorKey) == 0 {
		return reportmodel.ReportCatalog{}, reportError(500, "backend.report.catalog_unavailable", nil)
	}
	subject, err := s.resolveSubject(ctx, authority)
	if err != nil {
		return reportmodel.ReportCatalog{}, err
	}
	if !subject.HasPermission(reportsdk.ActionReportQueryExecute) {
		return reportmodel.ReportCatalog{}, reportError(403, "backend.permission.denied", nil)
	}
	size := request.Page.PageSize
	if size == 0 {
		size = reportmodel.ReportCatalogDefaultSize
	}
	if size < 1 || size > reportmodel.ReportCatalogMaximumSize {
		return reportmodel.ReportCatalog{}, reportError(400, "backend.report.page_size_invalid", nil)
	}
	reports, err := s.definitions.ReportDefinitions(ctx)
	if err != nil {
		return reportmodel.ReportCatalog{}, normalizeHostError(err, "backend.report.definition_read_failed")
	}
	reports = append([]reportmodel.ReportSchema(nil), reports...)
	sort.Slice(reports, func(i, j int) bool { return reports[i].Key < reports[j].Key })
	fingerprint := canonicalHash(struct {
		Reports []reportmodel.ReportSchema
		Scope   string
		Key     string
	}{reports, reportSnapshotAccessScopeHash(subject, nil), strings.TrimSpace(request.ReportKey)})
	position := 0
	if request.Page.Cursor != "" {
		if len(request.Page.Cursor) > 2048 {
			return reportmodel.ReportCatalog{}, reportError(400, "backend.report.cursor_invalid", nil)
		}
		content, err := base64.RawURLEncoding.DecodeString(request.Page.Cursor)
		var cursor catalogCursor
		if err != nil || json.Unmarshal(content, &cursor) != nil || cursor.Fingerprint != fingerprint || cursor.Position < 1 || cursor.Position >= len(reports) || !hmac.Equal([]byte(cursor.Proof), []byte(s.catalogProof(cursor))) {
			return reportmodel.ReportCatalog{}, reportError(400, "backend.report.cursor_invalid", nil)
		}
		position = cursor.Position
	}
	out := reportmodel.ReportCatalog{Reports: []reportmodel.ReportCatalogEntry{}}
	for index := position; index < len(reports); index++ {
		if err := ctx.Err(); err != nil {
			return reportmodel.ReportCatalog{}, err
		}
		report := reports[index]
		if key := strings.TrimSpace(request.ReportKey); key != "" && key != report.Key {
			continue
		}
		resolved := subject
		if resolved.TrustedProcess {
			resolved = resolved.WithExactProcessCapabilities(reportDataPermissionKeys(report)...)
		}
		if report.ObjectSQLV1 == nil || !reportVisibleToSubject(report, resolved) || !resolved.HasAllPermissions(reportDataPermissionKeys(report)) {
			continue
		}
		plan, err := s.compileAuthorizedObjectSQL(ctx, reportForDataPermissions(report, reportDataPermissionKeys(report)), resolved)
		if err != nil {
			var denied *reportsdk.Error
			if errors.As(err, &denied) && (denied.StatusCode == 403 || denied.StatusCode == 404) {
				continue
			}
			return reportmodel.ReportCatalog{}, err
		}
		if len(out.Reports) == size {
			cursor := catalogCursor{Fingerprint: fingerprint, Position: index}
			cursor.Proof = s.catalogProof(cursor)
			content, _ := json.Marshal(cursor)
			out.NextCursor, out.Truncated = base64.RawURLEncoding.EncodeToString(content), true
			break
		}
		// Deep-copy defaults as well: the catalog must not hand consumers a
		// mutable reference into the owner's published definition projection.
		parameters := make([]reportmodel.ReportObjectSQLParameter, len(report.ObjectSQLV1.Parameters))
		content, err := json.Marshal(report.ObjectSQLV1.Parameters)
		if err != nil {
			return reportmodel.ReportCatalog{}, reportError(500, "backend.report.definition_invalid", err)
		}
		decoder := json.NewDecoder(bytes.NewReader(content))
		decoder.UseNumber()
		if err := decoder.Decode(&parameters); err != nil {
			return reportmodel.ReportCatalog{}, reportError(500, "backend.report.definition_invalid", err)
		}
		if parameters == nil {
			parameters = []reportmodel.ReportObjectSQLParameter{}
		}
		out.Reports = append(out.Reports, reportmodel.ReportCatalogEntry{
			Key: report.Key, Name: report.Name, DefinitionVersion: canonicalHash(report),
			Parameters: parameters, ResultSchema: append([]reportmodel.ReportResultColumnSchema{}, plan.ResultSchema...), RowLimit: plan.Limit,
		})
	}
	return out, nil
}

func (s *QueryService) catalogProof(cursor catalogCursor) string {
	cursor.Proof = ""
	return s.queryProof("report-catalog-v1", cursor)
}
