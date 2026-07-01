package tools

import (
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
)

func TestBuildExcelCreateTableSQL_NoSheets(t *testing.T) {
	got := buildExcelCreateTableSQL("tbl", "/tmp/data.xlsx", nil)
	want := `CREATE TABLE "tbl" AS SELECT * FROM read_xlsx('/tmp/data.xlsx', header=true, all_varchar=true)`
	if got != want {
		t.Fatalf("mismatch.\n got: %s\nwant: %s", got, want)
	}
}

func TestBuildExcelCreateTableSQL_SingleSheetTagsSource(t *testing.T) {
	got := buildExcelCreateTableSQL("tbl", "/tmp/data.xlsx", []string{"Sheet1"})

	// Must use read_xlsx (excel extension) with explicit sheet param.
	if !strings.Contains(got, "FROM read_xlsx('/tmp/data.xlsx', sheet = 'Sheet1', header=true, all_varchar=true)") {
		t.Fatalf("expected read_xlsx with sheet param, got: %s", got)
	}
	// Must tag the source sheet name via the synthetic column so downstream
	// SQL behaves consistently between single- and multi-sheet workbooks.
	if !strings.Contains(got, "'Sheet1' AS "+excelSheetNameColumn) {
		t.Fatalf("expected sheet-name column, got: %s", got)
	}
}

func TestBuildExcelCreateTableSQL_MultiSheetUsesUnionAllByName(t *testing.T) {
	got := buildExcelCreateTableSQL("tbl", "/tmp/data.xlsx", []string{"Sheet1", "Sheet2", "报表"})

	// Each sheet must appear as a SELECT reading that specific sheet, and
	// the __sheet_name column must carry its name for per-sheet filtering.
	for _, sheet := range []string{"Sheet1", "Sheet2", "报表"} {
		needleRead := "FROM read_xlsx('/tmp/data.xlsx', sheet = '" + sheet + "', header=true, all_varchar=true)"
		needleTag := "'" + sheet + "' AS " + excelSheetNameColumn
		if !strings.Contains(got, needleRead) {
			t.Fatalf("missing read_xlsx for sheet %q in:\n%s", sheet, got)
		}
		if !strings.Contains(got, needleTag) {
			t.Fatalf("missing __sheet_name tag for sheet %q in:\n%s", sheet, got)
		}
	}

	// Must combine with UNION ALL BY NAME so schema drift between sheets is
	// tolerated.
	if !strings.Contains(got, "UNION ALL BY NAME") {
		t.Fatalf("expected UNION ALL BY NAME in multi-sheet SQL, got:\n%s", got)
	}

	// Exactly N-1 UNIONs for N sheets.
	if strings.Count(got, "UNION ALL BY NAME") != 2 {
		t.Fatalf("expected 2 UNION ALL BY NAME separators, got %d in:\n%s",
			strings.Count(got, "UNION ALL BY NAME"), got)
	}
}

func TestBuildExcelCreateTableSQL_EscapesSingleQuotes(t *testing.T) {
	// Sheet name and file path both contain single quotes, which must be
	// doubled to produce a valid SQL literal.
	sheets := []string{"Jo's data"}
	got := buildExcelCreateTableSQL("tbl", "/tmp/O'Brien/data.xlsx", sheets)

	if !strings.Contains(got, "sheet = 'Jo''s data'") {
		t.Fatalf("sheet name was not escaped, got:\n%s", got)
	}
	if !strings.Contains(got, "read_xlsx('/tmp/O''Brien/data.xlsx'") {
		t.Fatalf("file path was not escaped, got:\n%s", got)
	}
	if !strings.Contains(got, "'Jo''s data' AS "+excelSheetNameColumn) {
		t.Fatalf("sheet-name literal was not escaped, got:\n%s", got)
	}
}

func TestSqlSingleQuoteEscape(t *testing.T) {
	cases := map[string]string{
		"":               "",
		"no_quote":       "no_quote",
		"a'b":            "a''b",
		"''":             "''''",
		"mix'ed'quote":   "mix''ed''quote",
		"中文 with 'quote": "中文 with ''quote",
	}
	for in, want := range cases {
		if got := sqlSingleQuoteEscape(in); got != want {
			t.Errorf("sqlSingleQuoteEscape(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDataAnalysisDuplicateQueryCacheReturnsGuidance(t *testing.T) {
	tool := &DataAnalysisTool{queryCache: make(map[string]*types.ToolResult)}
	input := DataAnalysisInput{
		KnowledgeID: "knowledge-1",
		Sql:         "SELECT COUNT(*) FROM table_1",
	}
	tool.storeQueryResult(input, &types.ToolResult{
		Success: true,
		Output:  "Returned 1 rows\nrecord 1: {\"count\":3}",
		Data: map[string]interface{}{
			"display_type": ToolDataAnalysis,
			"row_count":    1,
		},
	})

	got := tool.getCachedQueryResult(DataAnalysisInput{
		KnowledgeID: "knowledge-1",
		Sql:         "SELECT   COUNT(*)   FROM   table_1",
	})

	if got == nil {
		t.Fatal("expected cached result")
	}
	if !strings.Contains(got.Output, "already been executed") {
		t.Fatalf("expected duplicate-call guidance, got: %s", got.Output)
	}
	if !strings.Contains(got.Output, "record 1") {
		t.Fatalf("expected cached rows to remain visible, got: %s", got.Output)
	}
}

func TestCompactToolOutputForHistory_DataAnalysisIncludesResultSummary(t *testing.T) {
	result := &types.ToolResult{
		Success: true,
		Data: map[string]interface{}{
			"display_type": ToolDataAnalysis,
			"query":        "SELECT COUNT(*) AS total FROM table_1",
			"row_count":    1,
			"rows": []map[string]string{
				{"total": "3"},
			},
		},
	}

	got := CompactToolOutputForHistory(ToolDataAnalysis, result)

	for _, want := range []string{
		"Data analysis returned 1 row(s)",
		"SELECT COUNT(*) AS total FROM table_1",
		"Sample rows:",
		"total",
		"3",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected %q in compact output:\n%s", want, got)
		}
	}
	if strings.Contains(got, "payload omitted from history") {
		t.Fatalf("data_analysis history must not collapse to generic omitted marker:\n%s", got)
	}
}
