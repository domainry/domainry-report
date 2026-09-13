package report

import (
	"context"
	"errors"
	"strings"
	"testing"

	sdk "github.com/domainry/domainry-report-sdk"
	model "github.com/domainry/domainry-report-sdk/model"
)

type sharedReadSubjects struct{}

func (sharedReadSubjects) ResolveReportSubject(_ context.Context, a model.ReportAuthority) (model.ReportSubject, error) {
	if a.Subject == nil {
		return model.ReportSubject{}, reportError(401, "auth.token_required", nil)
	}
	return *a.Subject, nil
}

type sharedQueryScopeFixture struct {
	queryReadScopeFixture
	denied bool
}

func (h *sharedQueryScopeFixture) AuthorizeSharedReportResultScope(_ context.Context, _ model.ReportSchema, producer, reader model.ReportSubject) error {
	if h.denied || producer.Principal.UserID != "producer" || reader.Principal.UserID != "reader" {
		return reportError(403, "backend.permission.denied", nil)
	}
	return nil
}

type sharedAnalysisScopeFixture struct {
	analysisReadScopeFixture
	denied bool
}

type restrictedSharedAnalysisCatalog struct {
	sharedAnalysisScopeFixture
	restriction string
}

func (h *restrictedSharedAnalysisCatalog) ReportAnalysisSources(ctx context.Context, subject model.ReportSubject) ([]model.AnalysisDataset, error) {
	datasets, err := h.analysisExecutor.ReportAnalysisSources(ctx, subject)
	if err != nil || subject.Principal.UserID != "reader" {
		return datasets, err
	}
	if h.restriction == "unavailable" {
		return nil, reportError(503, "test.catalog_unavailable", nil)
	}
	if h.restriction == "dataset" {
		return nil, nil
	}
	if h.restriction == "field" {
		filtered := append([]model.AnalysisDataset{}, datasets...)
		for i, dataset := range filtered {
			filtered[i].Columns = nil
			for _, column := range dataset.Columns {
				if column.Key != "category" {
					filtered[i].Columns = append(filtered[i].Columns, column)
				}
			}
		}
		return filtered, nil
	}
	return datasets, nil
}

func (h *sharedAnalysisScopeFixture) AuthorizeSharedReportResultScope(_ context.Context, _ model.ReportSchema, producer, reader model.ReportSubject) error {
	if h.denied || producer.Principal.UserID != "producer" || reader.Principal.UserID != "reader" {
		return reportError(403, "backend.permission.denied", nil)
	}
	return nil
}
func sharedReadAuthority(user string, permissions ...string) model.ReportAuthority {
	s := applicationTestSubjectWithPermissions(permissions...)
	s.Principal.UserID = user
	return model.ReportAuthority{Subject: &s}
}

func TestSharedReportReadAuthenticatesProducerWithoutGrantingReadersExecution(t *testing.T) {
	s, e, v, _ := evidenceFixture()
	h := &sharedQueryScopeFixture{queryReadScopeFixture: queryReadScopeFixture{e, strings.Repeat("a", 64)}}
	s.objectSQL, s.subjects = h, sharedReadSubjects{}
	origin := sharedReadAuthority("producer", sdk.ActionReportQueryExecute, "event.read")
	query := model.ReportObjectSQLRequest{ReportKey: "sales"}
	result, err := s.Query(t.Context(), query, origin)
	if err != nil {
		t.Fatal(err)
	}
	catRequest := model.ReportCatalogRequest{}
	catalog, err := s.Catalog(t.Context(), catRequest, origin)
	if err != nil {
		t.Fatal(err)
	}
	a := sharedReadAuthority("reader", sdk.ActionReportResultsRead, "event.read")
	in := model.ReportQueryResultAuthorization{Query: query, Result: result}
	if err := s.AuthorizeQueryResultRead(t.Context(), in, a); err == nil {
		t.Fatal("ordinary read accepted another producer")
	}
	if err := s.AuthorizeSharedQueryResultRead(t.Context(), in, a, origin); err != nil {
		t.Fatal(err)
	}
	if err := s.AuthorizeSharedCatalogRead(t.Context(), model.ReportCatalogReadAuthorization{Request: catRequest, Result: catalog}, a, origin); err != nil {
		t.Fatal(err)
	}
	origin = sharedReadAuthority("producer", "event.read")
	if err := s.AuthorizeSharedQueryResultRead(t.Context(), in, a, origin); err != nil {
		t.Fatal("producer query revocation invalidated readable evidence", err)
	}
	if _, err := s.Query(t.Context(), query, a); err == nil {
		t.Fatal("reading granted execution")
	}
	if err := s.AuthorizeQueryResult(t.Context(), in, a); err == nil {
		t.Fatal("reading granted ordinary replay")
	}
	for _, change := range []string{"body", "producer", "read", "data", "scope", "version", "workspace"} {
		t.Run(change, func(t *testing.T) {
			current := in
			reader := a
			producer := origin
			switch change {
			case "body":
				current.Result.Source.Complete = !current.Result.Source.Complete
			case "producer":
				producer = sharedReadAuthority("other", "event.read")
			case "read":
				reader = sharedReadAuthority("reader", "event.read")
			case "data":
				reader = sharedReadAuthority("reader", sdk.ActionReportResultsRead)
			case "scope":
				h.denied = true
			case "version":
				v.version = "changed"
			case "workspace":
				reader = sharedReadAuthority("reader", sdk.ActionReportResultsRead, "event.read")
				reader.Subject.Principal.WorkspaceID = "other"
			}
			if err := s.AuthorizeSharedQueryResultRead(t.Context(), current, reader, producer); err == nil {
				t.Fatal("invalid shared evidence accepted", change)
			}
			h.denied = false
			v.version = "v1"
		})
	}
	if len(e.requests) != 1 {
		t.Fatal("reading reexecuted query", len(e.requests))
	}
}

func TestSharedAnalysisAndCatalogAuthenticateOriginalProofAndActualReader(t *testing.T) {
	s, e, _, _, request := analysisFixture()
	h := &sharedAnalysisScopeFixture{analysisReadScopeFixture: analysisReadScopeFixture{e, strings.Repeat("c", 64)}}
	s.objectSQL, s.subjects = h, sharedReadSubjects{}
	origin := sharedReadAuthority("producer", sdk.ActionReportQueryExecute, "event.read", "order.read")
	result, err := s.RunAnalysis(t.Context(), request, origin)
	if err != nil {
		t.Fatal(err)
	}
	catRequest := model.AnalysisCatalogRequest{DatasetKey: " " + request.DatasetKey + " "}
	catalog, err := s.AnalysisCatalog(t.Context(), catRequest, origin)
	if err != nil {
		t.Fatal(err)
	}
	a := sharedReadAuthority("reader", sdk.ActionReportResultsRead, "event.read", "order.read")
	in := model.AnalysisResultAuthorization{Request: request, Result: result}
	if err := s.AuthorizeSharedAnalysisResultRead(t.Context(), in, a, origin); err != nil {
		t.Fatal(err)
	}
	if err := s.AuthorizeSharedAnalysisCatalogRead(t.Context(), model.AnalysisCatalogReadAuthorization{Request: catRequest, Result: catalog}, a, origin); err != nil {
		t.Fatal(err)
	}
	origin = sharedReadAuthority("producer", "event.read", "order.read")
	if err := s.AuthorizeSharedAnalysisResultRead(t.Context(), in, a, origin); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RunAnalysis(t.Context(), request, a); err == nil {
		t.Fatal("reading granted analysis execution")
	}
	h.denied = true
	if err := s.AuthorizeSharedAnalysisResultRead(t.Context(), in, a, origin); err == nil {
		t.Fatal("narrower reader accepted original aggregation")
	}
	h.denied = false
	in.Result.Source.Complete = !in.Result.Source.Complete
	if err := s.AuthorizeSharedAnalysisResultRead(t.Context(), in, a, origin); err == nil {
		t.Fatal("altered analysis proof accepted")
	}
}

func TestSharedAnalysisAuthenticatedOriginalSpecClassifiesHiddenReaderSourcesAsDenied(t *testing.T) {
	s, executor, _, _, request := analysisFixture()
	h := &restrictedSharedAnalysisCatalog{sharedAnalysisScopeFixture: sharedAnalysisScopeFixture{analysisReadScopeFixture: analysisReadScopeFixture{executor, strings.Repeat("c", 64)}}}
	s.objectSQL, s.subjects = h, sharedReadSubjects{}
	producer := sharedReadAuthority("producer", sdk.ActionReportQueryExecute, "event.read", "order.read")
	reader := sharedReadAuthority("reader", sdk.ActionReportResultsRead, "event.read", "order.read")
	result, err := s.RunAnalysis(t.Context(), request, producer)
	if err != nil {
		t.Fatal(err)
	}
	for _, restriction := range []string{"field", "dataset", "unavailable"} {
		t.Run(restriction, func(t *testing.T) {
			h.restriction = restriction
			var failure *sdk.Error
			err := s.AuthorizeSharedAnalysisResultRead(t.Context(), model.AnalysisResultAuthorization{Request: request, Result: result}, reader, producer)
			want := 403
			if restriction == "unavailable" {
				want = 503
			}
			if !errors.As(err, &failure) || failure.StatusCode != want {
				t.Fatal("authorized catalog restriction had the wrong classification", err)
			}
		})
	}
	h.restriction = "field"
	altered := result
	altered.Source.Complete = !altered.Source.Complete
	var failure *sdk.Error
	if err := s.AuthorizeSharedAnalysisResultRead(t.Context(), model.AnalysisResultAuthorization{Request: request, Result: altered}, reader, producer); !errors.As(err, &failure) || failure.StatusCode != 409 {
		t.Fatal("hidden field classification bypassed original proof validation", err)
	}
	invalid := request
	invalid.GroupBy = []string{"not_a_source_field"}
	if err := s.AuthorizeSharedAnalysisResultRead(t.Context(), model.AnalysisResultAuthorization{Request: invalid, Result: result}, reader, producer); !errors.As(err, &failure) || failure.StatusCode != 400 {
		t.Fatal("a truly invalid original specification was misclassified as permission denial", err)
	}
	if len(executor.requests) != 1 {
		t.Fatal("denied or invalid shared reads reexecuted analysis", len(executor.requests))
	}
}

func TestSharedCatalogReadRejectsMissingSigningKey(t *testing.T) {
	s, e, _, _ := evidenceFixture()
	s.objectSQL, s.subjects = &sharedQueryScopeFixture{queryReadScopeFixture: queryReadScopeFixture{e, strings.Repeat("a", 64)}}, sharedReadSubjects{}
	producer := sharedReadAuthority("producer", sdk.ActionReportQueryExecute, "event.read")
	reader := sharedReadAuthority("reader", sdk.ActionReportResultsRead, "event.read")
	request := model.ReportCatalogRequest{}
	result, err := s.Catalog(t.Context(), request, producer)
	if err != nil {
		t.Fatal(err)
	}
	definitions, err := s.definitions.ReportDefinitions(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	s.cursorKey = nil
	// This is the valid HMAC an attacker could compute if an unconfigured
	// owner accidentally accepted the publicly known empty signing key.
	result.ReadProof = s.catalogReadProof(request, result, definitions, *producer.Subject)
	if err := s.AuthorizeSharedCatalogRead(t.Context(), model.ReportCatalogReadAuthorization{Request: request, Result: result}, reader, producer); err == nil {
		t.Fatal("unconfigured owner accepted empty-key proof")
	}
	if err := s.AuthorizeSharedAnalysisCatalogRead(t.Context(), model.AnalysisCatalogReadAuthorization{}, reader, producer); err == nil {
		t.Fatal("unconfigured analysis owner accepted catalog")
	}
}

func TestSharedResultReadRequiresOwnerScopeAttestation(t *testing.T) {
	s, _, _, _ := evidenceFixture()
	s.subjects = sharedReadSubjects{}
	producer := sharedReadAuthority("producer", sdk.ActionReportQueryExecute, "event.read")
	reader := sharedReadAuthority("reader", sdk.ActionReportResultsRead, "event.read")
	query := model.ReportObjectSQLRequest{ReportKey: "sales"}
	result, err := s.Query(t.Context(), query, producer)
	if err != nil {
		t.Fatal(err)
	}
	var unavailable *sdk.Error
	if err := s.AuthorizeSharedQueryResultRead(t.Context(), model.ReportQueryResultAuthorization{Query: query, Result: result}, reader, producer); !errors.As(err, &unavailable) || unavailable.StatusCode != 503 {
		t.Fatal("missing owner scope was not classified unavailable", err)
	}
	analysis, _, _, _, request := analysisFixture()
	analysis.subjects = sharedReadSubjects{}
	producer = sharedReadAuthority("producer", sdk.ActionReportQueryExecute, "event.read", "order.read")
	reader = sharedReadAuthority("reader", sdk.ActionReportResultsRead, "event.read", "order.read")
	computed, err := analysis.RunAnalysis(t.Context(), request, producer)
	if err != nil {
		t.Fatal(err)
	}
	if err := analysis.AuthorizeSharedAnalysisResultRead(t.Context(), model.AnalysisResultAuthorization{Request: request, Result: computed}, reader, producer); !errors.As(err, &unavailable) || unavailable.StatusCode != 503 {
		t.Fatal("missing analysis scope was not classified unavailable", err)
	}
}
