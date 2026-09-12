package module_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"

	reportsdk "github.com/domainry/domainry-report-sdk"
	model "github.com/domainry/domainry-report-sdk/model"
	"github.com/domainry/domainry-report-sdk/modulehost"
	reportmodule "github.com/domainry/domainry-report/module"
)

// A separate source implements only public table contracts. No SQL, Knowledge
// implementation, Agent implementation or storage handle is given to it.
type publicTableSource struct {
	denied  bool
	streams int
	version string
}

func (s *publicTableSource) ReportAnalysisSources(ctx context.Context, subject model.ReportSubject) ([]model.AnalysisDataset, error) {
	if s.denied || subject.Principal.UserID != "user-a" {
		return []model.AnalysisDataset{}, nil
	}
	return []model.AnalysisDataset{{Key: "source_table", Name: "Governed structured table", Kind: "table_file", Version: "table-definition-1", Columns: []model.AnalysisColumn{{Key: "dept", Type: "text"}, {Key: "amount", Type: "decimal", Unit: "CNY", Scale: 2}}}}, ctx.Err()
}
func (s *publicTableSource) ReadReportAnalysisTableVersion(ctx context.Context, key string, fields []string, subject model.ReportSubject) (modulehost.AnalysisTableVersion, error) {
	if s.denied || key != "source_table" || subject.Principal.UserID != "user-a" || subject.Principal.WorkspaceID != "workspace-a" {
		return modulehost.AnalysisTableVersion{}, &reportsdk.Error{StatusCode: 403, Code: "backend.permission.denied"}
	}
	for _, field := range fields {
		if field != "dept" && field != "amount" {
			return modulehost.AnalysisTableVersion{}, &reportsdk.Error{StatusCode: 403, Code: "backend.permission.denied"}
		}
	}
	digest := sha256.New()
	encoder := json.NewEncoder(digest)
	_ = encoder.Encode(fields)
	for i := 0; i < 1501; i++ {
		dept, amount := "研发", "0.01"
		if i == 1500 {
			amount = "9007199254740993.25"
		}
		row := model.AnalysisTableRow{"dept": &dept, "amount": &amount}
		values := make([]*string, 0, len(fields))
		for _, field := range fields {
			values = append(values, row[field])
		}
		_ = encoder.Encode(values)
	}
	return modulehost.AnalysisTableVersion{DatasetKey: key, DefinitionVersion: "table-definition-1", DataVersion: s.version, ContentSHA256: hex.EncodeToString(digest.Sum(nil)), Rows: 1501, Complete: true}, ctx.Err()
}
func (s *publicTableSource) StreamReportAnalysisTable(ctx context.Context, expected modulehost.AnalysisTableVersion, fields []string, subject model.ReportSubject, consume func(model.AnalysisTableRow) error) (modulehost.AnalysisTableVersion, error) {
	v, err := s.ReadReportAnalysisTableVersion(ctx, expected.DatasetKey, fields, subject)
	if err != nil {
		return v, err
	}
	s.streams++
	for i := 0; i < 1501; i++ {
		dept, amount := "研发", "0.01"
		if i == 1500 {
			amount = "9007199254740993.25"
		}
		all := model.AnalysisTableRow{"dept": &dept, "amount": &amount}
		row := model.AnalysisTableRow{}
		for _, key := range fields {
			row[key] = all[key]
		}
		if err := consume(row); err != nil {
			return modulehost.AnalysisTableVersion{}, err
		}
	}
	return s.ReadReportAnalysisTableVersion(ctx, expected.DatasetKey, fields, subject)
}

type publicTableHost struct {
	*integrationHost
	table *publicTableSource
}

func (h *publicTableHost) ReportAnalysisTables() modulehost.AnalysisTableSource { return h.table }

func TestPublicModuleBindsIndependentStructuredTableSourceAndReauthorizesSavedResult(t *testing.T) {
	base := newIntegrationHost(t)
	sum := sha256.Sum256([]byte("synthetic structured service revision; 1501 rows"))
	source := &publicTableSource{version: hex.EncodeToString(sum[:])}
	host := &publicTableHost{integrationHost: base, table: source}
	open := func() reportsdk.Analyses {
		binding, err := reportmodule.NewFactory().Open(t.Context(), reportsdk.ApplicationRef{RuntimeID: "file-analysis-integration"}, host)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = binding.Close(context.Background()) })
		if err := binding.(reportsdk.ApplicationHostBinder).BindApplicationHost(host); err != nil {
			t.Fatal(err)
		}
		return binding.(reportsdk.ApplicationBinding).Queries().(reportsdk.Analyses)
	}
	api := open()
	authority := model.ReportAuthority{AccessToken: "workspace-a-full"}
	request := model.AnalysisRequest{DatasetKey: "source_table", GroupBy: []string{"dept"}, Measures: []model.AnalysisMeasure{{Key: "total", Field: "amount", Function: "sum"}}}
	catalog, err := api.AnalysisCatalog(t.Context(), model.AnalysisCatalogRequest{}, authority)
	if err != nil || len(catalog.Datasets) != 1 {
		t.Fatal(catalog, err)
	}
	out, err := api.RunAnalysis(t.Context(), request, authority)
	if err != nil || len(out.Rows) != 1 || *out.Rows[0].Values["total"] != "9007199254741008.25" || out.Source.InputCounts["dataset"] != "1501" || !out.Source.Complete {
		t.Fatal(out, err)
	}
	if base.objectSQLExecutions.Load() != 0 || source.streams != 1 {
		t.Fatal("table crossed SQL or ran repeatedly")
	}
	raw, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	var saved model.AnalysisResult
	if err = json.Unmarshal(raw, &saved); err != nil {
		t.Fatal(err)
	}
	api = open()
	if err = api.AuthorizeAnalysisResult(t.Context(), model.AnalysisResultAuthorization{Request: request, Result: saved}, authority); err != nil || source.streams != 1 {
		t.Fatal(err, source.streams)
	}
	source.denied = true
	if err = api.AuthorizeAnalysisResult(t.Context(), model.AnalysisResultAuthorization{Request: request, Result: saved}, authority); err == nil {
		t.Fatal("revoked file result readable")
	}
	source.denied = false
	source.version = "updated-service-revision"
	if err = api.AuthorizeAnalysisResult(t.Context(), model.AnalysisResultAuthorization{Request: request, Result: saved}, authority); err == nil {
		t.Fatal("stale source readable")
	}
}

var _ modulehost.AnalysisTableSource = (*publicTableSource)(nil)
var _ modulehost.AnalysisTableHost = (*publicTableHost)(nil)
