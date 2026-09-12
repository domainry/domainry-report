package report

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"

	model "github.com/domainry/domainry-report-sdk/model"
	analysis "github.com/domainry/domainry-report/internal/domain/report/service/analysis"
)

func (s *QueryService) analysisTableState(ctx context.Context, state analysisState) (analysisState, error) {
	host := s.tables
	if host == nil {
		return analysisState{}, reportError(503, "backend.report.analysis.table_unavailable", nil)
	}
	// Validate typed filter values before asking the owner to open any stream.
	if _, err := analysis.NewTableAccumulator(state.plan); err != nil {
		return analysisState{}, analysisError(err)
	}
	version, err := host.ReadReportAnalysisTableVersion(ctx, state.plan.Dataset.Key, analysis.TableFields(state.plan), state.subject)
	if err != nil {
		return analysisState{}, normalizeHostError(err, "backend.report.analysis.source_version_failed")
	}
	if !version.Complete {
		return analysisState{}, reportError(422, "backend.report.analysis.source_incomplete", nil)
	}
	if version.DatasetKey != state.plan.Dataset.Key || version.DefinitionVersion != state.plan.Dataset.Version || strings.TrimSpace(version.DataVersion) == "" || len(version.DataVersion) > 256 || version.Rows < 0 {
		return analysisState{}, reportError(502, "backend.report.analysis.result_invalid", nil)
	}
	if digest, err := hex.DecodeString(version.ContentSHA256); err != nil || len(digest) != sha256.Size || version.ContentSHA256 != strings.ToLower(version.ContentSHA256) {
		return analysisState{}, reportError(502, "backend.report.analysis.result_invalid", nil)
	}
	if version.Rows > analysis.MaximumTableRows {
		return analysisState{}, reportError(422, "backend.report.analysis.result_limit_exceeded", nil)
	}
	state.table = &version
	return state, nil
}

func (s *QueryService) evaluateAnalysisTable(ctx context.Context, state analysisState) (analysis.Evaluation, error) {
	host := s.tables
	acc, err := analysis.NewTableAccumulator(state.plan)
	if err != nil {
		return analysis.Evaluation{}, analysisError(err)
	}
	// Keep the first consumer failure even if a faulty host ignores it. Never
	// release already accumulated rows on a partial or failed stream.
	var consumeErr error
	fields := analysis.TableFields(state.plan)
	digest := sha256.New()
	encoder := json.NewEncoder(digest)
	_ = encoder.Encode(fields) // hash.Hash.Write cannot fail; fields are strings.
	version, err := host.StreamReportAnalysisTable(ctx, *state.table, fields, state.subject, func(row model.AnalysisTableRow) error {
		if consumeErr != nil {
			return consumeErr
		}
		consumeErr = acc.Add(ctx, row)
		if consumeErr == nil {
			values := make([]*string, 0, len(fields))
			for _, field := range fields {
				values = append(values, row[field])
			}
			consumeErr = encoder.Encode(values)
		}
		return consumeErr
	})
	if ctx.Err() != nil {
		return analysis.Evaluation{}, ctx.Err()
	}
	if consumeErr != nil {
		return analysis.Evaluation{}, analysisError(consumeErr)
	}
	if err != nil {
		return analysis.Evaluation{}, normalizeHostError(err, "backend.report.analysis.execution_failed")
	}
	if version != *state.table || acc.Rows() != version.Rows || hex.EncodeToString(digest.Sum(nil)) != version.ContentSHA256 {
		return analysis.Evaluation{}, reportError(409, "backend.report.analysis.source_changed", nil)
	}
	out, err := acc.Evaluate()
	if err != nil {
		return analysis.Evaluation{}, analysisError(err)
	}
	return out, nil
}
