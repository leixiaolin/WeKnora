package tools

import (
	"context"
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"unicode"

	filesvc "github.com/Tencent/WeKnora/internal/application/service/file"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/Tencent/WeKnora/internal/utils"
	"github.com/xuri/excelize/v2"
)

var dataAnalysisTool = BaseTool{
	name: ToolDataAnalysis,
	description: "Use this tool when the knowledge is CSV or Excel files. It loads the data into memory and executes SQL for data analysis. " +
		"For Excel files with multiple sheets, every sheet is loaded into the same table and the source sheet name is exposed as a '__sheet_name' column so you can filter/aggregate per sheet. " +
		"If the user's question requires data statistics, convert the question into SQL and execute it.",
	schema: utils.GenerateSchema[DataAnalysisInput](),
}

// excelSheetNameColumn is the name of the synthetic column that identifies
// which Excel sheet a row came from when multiple sheets are unioned together.
const (
	excelSheetNameColumn = "__sheet_name"
	excelRowNumberColumn = "__row_number"
)

const (
	excelHeaderScanRows      = 40
	excelHeaderMinConfidence = 3.0
	excelMaxSampleRows       = 3
)

// sqlSingleQuoteEscape escapes single quotes in a string so it can be safely
// embedded inside a single-quoted SQL literal.
func sqlSingleQuoteEscape(s string) string {
	return strings.ReplaceAll(s, "'", "''")
}

func normalizeIdentifierForMatch(s string) string {
	normalized := strings.ToLower(strings.TrimSpace(s))
	normalized = strings.ReplaceAll(normalized, " ", "")
	normalized = strings.ReplaceAll(normalized, "\u3000", "")
	return normalized
}

func reconcileSQLColumnsWithSchema(sqlText string, schema *TableSchema) (string, []string) {
	if schema == nil || len(schema.Columns) == 0 {
		return sqlText, nil
	}

	normalizedToCanonical := make(map[string]string, len(schema.Columns))
	for _, col := range schema.Columns {
		key := normalizeIdentifierForMatch(col.Name)
		if key == "" {
			continue
		}
		if _, exists := normalizedToCanonical[key]; !exists {
			normalizedToCanonical[key] = col.Name
		}
	}

	quotedIdentifierPattern := regexp.MustCompile(`"([^"]+)"`)
	fixes := make([]string, 0)
	rewritten := quotedIdentifierPattern.ReplaceAllStringFunc(sqlText, func(token string) string {
		name := strings.Trim(token, "\"")
		canonical, ok := normalizedToCanonical[normalizeIdentifierForMatch(name)]
		if !ok || canonical == name {
			return token
		}
		fixes = append(fixes, fmt.Sprintf("%q -> %q", name, canonical))
		return fmt.Sprintf(`"%s"`, canonical)
	})

	return rewritten, fixes
}

func buildMissingColumnSuggestion(sqlErr error, schema *TableSchema) string {
	if sqlErr == nil || schema == nil {
		return ""
	}
	msg := sqlErr.Error()
	if !strings.Contains(msg, `Referenced column "`) || !strings.Contains(msg, `not found`) {
		return ""
	}

	matches := regexp.MustCompile(`Referenced column "([^"]+)" not found`).FindStringSubmatch(msg)
	if len(matches) < 2 {
		return ""
	}

	missing := matches[1]
	normalizedMissing := normalizeIdentifierForMatch(missing)
	if normalizedMissing == "" {
		return ""
	}

	for _, col := range schema.Columns {
		if normalizeIdentifierForMatch(col.Name) == normalizedMissing {
			return fmt.Sprintf("Column %q does not exist. Did you mean %q? Please use the exact column name from schema.", missing, col.Name)
		}
	}

	return ""
}

type excelSheetDiagnostic struct {
	Name       string   `json:"name"`
	HeaderRow  int      `json:"header_row"`
	DataRows   int      `json:"data_rows"`
	Columns    []string `json:"columns"`
	SampleRows []string `json:"sample_rows,omitempty"`
	Confidence float64  `json:"confidence"`
	Normalized bool     `json:"normalized"`
	SkipReason string   `json:"skip_reason,omitempty"`
}

type normalizedExcelSheet struct {
	diagnostic excelSheetDiagnostic
	columns    []string
	rows       []normalizedExcelRow
}

type normalizedExcelRow struct {
	rowNumber int
	values    map[string]string
}

type inferredExcelHeader struct {
	rowIndex    int
	confidence  float64
	dataStart   int
	activeWidth int
}

var excelHeaderKeywords = []string{
	"编号", "姓名", "名称", "专业", "代码", "成绩", "日期", "时间", "状态", "类别",
	"类型", "金额", "数量", "分数", "排名", "备注", "学院", "院系", "方式", "序号",
	"id", "name", "code", "date", "time", "status", "type", "amount",
	"count", "score", "rank", "category", "major", "no", "number",
}

func trimExcelCell(s string) string {
	return strings.TrimSpace(strings.Join(strings.Fields(s), " "))
}

func excelRowNonEmptyCount(row []string, width int) int {
	count := 0
	for i := 0; i < width && i < len(row); i++ {
		if trimExcelCell(row[i]) != "" {
			count++
		}
	}
	return count
}

func excelRowLastNonEmptyIndex(row []string) int {
	for i := len(row) - 1; i >= 0; i-- {
		if trimExcelCell(row[i]) != "" {
			return i
		}
	}
	return -1
}

func excelRowsMaxWidth(rows [][]string) int {
	width := 0
	for _, row := range rows {
		if len(row) > width {
			width = len(row)
		}
	}
	return width
}

func isExcelNumericLike(s string) bool {
	s = strings.TrimSpace(strings.ReplaceAll(s, ",", ""))
	if s == "" {
		return false
	}
	s = strings.TrimSuffix(s, "%")
	if s == "" {
		return false
	}
	_, err := strconv.ParseFloat(s, 64)
	return err == nil
}

func isExcelTextLike(s string) bool {
	s = trimExcelCell(s)
	if s == "" || isExcelNumericLike(s) {
		return false
	}
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.Is(unicode.Han, r) {
			return true
		}
	}
	return false
}

func excelHeaderKeywordHits(row []string) int {
	hits := 0
	seen := make(map[string]bool)
	for _, cell := range row {
		text := strings.ToLower(trimExcelCell(cell))
		if text == "" {
			continue
		}
		for _, keyword := range excelHeaderKeywords {
			if strings.Contains(text, keyword) && !seen[keyword] {
				hits++
				seen[keyword] = true
			}
		}
	}
	return hits
}

func scoreExcelHeaderCandidate(rows [][]string, rowIndex, width int) (float64, int) {
	row := rows[rowIndex]
	nonEmpty := excelRowNonEmptyCount(row, width)
	if width == 0 || nonEmpty < 2 {
		return 0, 0
	}

	unique := make(map[string]bool)
	textCount := 0
	numericCount := 0
	lastHeaderCell := excelRowLastNonEmptyIndex(row)
	for i := 0; i < width && i < len(row); i++ {
		cell := trimExcelCell(row[i])
		if cell == "" {
			continue
		}
		unique[normalizeIdentifierForMatch(cell)] = true
		if isExcelTextLike(cell) {
			textCount++
		}
		if isExcelNumericLike(cell) {
			numericCount++
		}
	}
	if lastHeaderCell < 0 {
		return 0, 0
	}

	fillRatio := float64(nonEmpty) / float64(width)
	uniqueRatio := float64(len(unique)) / float64(nonEmpty)
	textRatio := float64(textCount) / float64(nonEmpty)
	score := fillRatio*2 + uniqueRatio*1.5 + textRatio

	if numericCount == nonEmpty {
		score -= 3
	}
	if hits := excelHeaderKeywordHits(row); hits > 0 {
		if hits > 5 {
			hits = 5
		}
		score += float64(hits) * 1.1
	}
	if rowIndex > 0 && excelRowNonEmptyCount(rows[rowIndex-1], width) == 1 && nonEmpty >= 2 {
		score += 2
	}

	dataRows := 0
	dataShapeBonus := 0.0
	maxDataCell := lastHeaderCell
	for i := rowIndex + 1; i < len(rows) && dataRows < 5; i++ {
		if last := excelRowLastNonEmptyIndex(rows[i]); last > maxDataCell {
			maxDataCell = last
		}
		nextNonEmpty := excelRowNonEmptyCount(rows[i], lastHeaderCell+1)
		if nextNonEmpty == 0 {
			continue
		}
		dataRows++
		if nextNonEmpty >= maxInt(1, nonEmpty/2) {
			dataShapeBonus += 0.4
		}
	}
	if dataRows > 0 {
		score += 1.5 + dataShapeBonus
	}

	return score, maxDataCell + 1
}

func inferExcelHeader(rows [][]string) (inferredExcelHeader, bool) {
	width := excelRowsMaxWidth(rows)
	if width == 0 {
		return inferredExcelHeader{}, false
	}

	best := inferredExcelHeader{rowIndex: -1}
	limit := len(rows)
	if limit > excelHeaderScanRows {
		limit = excelHeaderScanRows
	}
	for i := 0; i < limit; i++ {
		score, activeWidth := scoreExcelHeaderCandidate(rows, i, width)
		if score > best.confidence {
			best = inferredExcelHeader{
				rowIndex:    i,
				confidence:  score,
				dataStart:   i + 1,
				activeWidth: activeWidth,
			}
		}
	}
	if best.rowIndex < 0 || best.confidence < excelHeaderMinConfidence || best.activeWidth == 0 {
		return inferredExcelHeader{}, false
	}
	return best, true
}

func normalizeExcelColumns(header []string, width int) []string {
	columns := make([]string, 0, width)
	used := make(map[string]int)
	for i := 0; i < width; i++ {
		name := ""
		if i < len(header) {
			name = trimExcelCell(header[i])
		}
		if name == "" {
			name = fmt.Sprintf("column_%d", i+1)
		}
		if normalizeIdentifierForMatch(name) == excelSheetNameColumn ||
			normalizeIdentifierForMatch(name) == excelRowNumberColumn {
			name = name + "_value"
		}

		key := normalizeIdentifierForMatch(name)
		used[key]++
		if used[key] > 1 {
			name = fmt.Sprintf("%s__%d", name, used[key])
		}
		columns = append(columns, name)
	}
	return columns
}

func normalizeExcelSheet(f *excelize.File, sheetName string) (*normalizedExcelSheet, error) {
	rows, err := f.GetRows(sheetName, excelize.Options{RawCellValue: true})
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 || excelRowsMaxWidth(rows) == 0 {
		return &normalizedExcelSheet{
			diagnostic: excelSheetDiagnostic{
				Name:       sheetName,
				Normalized: false,
				SkipReason: "empty sheet",
			},
		}, nil
	}

	header, ok := inferExcelHeader(rows)
	if !ok {
		return nil, fmt.Errorf("could not confidently infer header row for sheet %q", sheetName)
	}

	columns := normalizeExcelColumns(rows[header.rowIndex], header.activeWidth)
	sheet := &normalizedExcelSheet{
		columns: columns,
		diagnostic: excelSheetDiagnostic{
			Name:       sheetName,
			HeaderRow:  header.rowIndex + 1,
			Columns:    append([]string(nil), columns...),
			Confidence: header.confidence,
			Normalized: true,
		},
	}

	for rowIndex := header.dataStart; rowIndex < len(rows); rowIndex++ {
		row := rows[rowIndex]
		if excelRowNonEmptyCount(row, header.activeWidth) == 0 {
			continue
		}
		values := make(map[string]string, len(columns))
		sampleParts := make([]string, 0, len(columns))
		for colIndex, colName := range columns {
			value := ""
			if colIndex < len(row) {
				value = trimExcelCell(row[colIndex])
			}
			values[colName] = value
			if len(sheet.diagnostic.SampleRows) < excelMaxSampleRows && value != "" {
				sampleParts = append(sampleParts, fmt.Sprintf("%s=%s", colName, value))
			}
		}
		sheet.rows = append(sheet.rows, normalizedExcelRow{
			rowNumber: rowIndex + 1,
			values:    values,
		})
		if len(sheet.diagnostic.SampleRows) < excelMaxSampleRows && len(sampleParts) > 0 {
			sheet.diagnostic.SampleRows = append(sheet.diagnostic.SampleRows, strings.Join(sampleParts, ", "))
		}
	}
	sheet.diagnostic.DataRows = len(sheet.rows)
	return sheet, nil
}

func normalizeExcelWorkbook(filename string) ([]normalizedExcelSheet, []string, []excelSheetDiagnostic, error) {
	f, err := excelize.OpenFile(filename)
	if err != nil {
		return nil, nil, nil, err
	}
	defer func() { _ = f.Close() }()

	globalColumns := make([]string, 0)
	seenColumns := make(map[string]bool)
	sheets := make([]normalizedExcelSheet, 0)
	diagnostics := make([]excelSheetDiagnostic, 0)
	for _, sheetName := range f.GetSheetList() {
		if strings.TrimSpace(sheetName) == "" {
			continue
		}
		sheet, err := normalizeExcelSheet(f, sheetName)
		if err != nil {
			return nil, nil, nil, err
		}
		diagnostics = append(diagnostics, sheet.diagnostic)
		if !sheet.diagnostic.Normalized {
			continue
		}
		sheets = append(sheets, *sheet)
		for _, col := range sheet.columns {
			key := normalizeIdentifierForMatch(col)
			if seenColumns[key] {
				continue
			}
			seenColumns[key] = true
			globalColumns = append(globalColumns, col)
		}
	}
	if len(sheets) == 0 || len(globalColumns) == 0 {
		return nil, nil, diagnostics, fmt.Errorf("no non-empty Excel sheets could be normalized")
	}
	return sheets, globalColumns, diagnostics, nil
}

func writeNormalizedExcelCSV(sheets []normalizedExcelSheet, globalColumns []string) (string, func(), error) {
	file, err := os.CreateTemp("", "weknora-excel-*.csv")
	if err != nil {
		return "", nil, err
	}
	path := file.Name()
	cleanup := func() { _ = os.Remove(path) }

	writer := csv.NewWriter(file)
	header := append([]string{}, globalColumns...)
	header = append(header, excelSheetNameColumn, excelRowNumberColumn)
	if err := writer.Write(header); err != nil {
		_ = file.Close()
		cleanup()
		return "", nil, err
	}

	for _, sheet := range sheets {
		for _, row := range sheet.rows {
			record := make([]string, 0, len(header))
			for _, col := range globalColumns {
				record = append(record, row.values[col])
			}
			record = append(record, sheet.diagnostic.Name, strconv.Itoa(row.rowNumber))
			if err := writer.Write(record); err != nil {
				_ = file.Close()
				cleanup()
				return "", nil, err
			}
		}
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		_ = file.Close()
		cleanup()
		return "", nil, err
	}
	if err := file.Close(); err != nil {
		cleanup()
		return "", nil, err
	}
	return path, cleanup, nil
}

func createNormalizedExcelTable(ctx context.Context, db *sql.DB, filename, tableName string) ([]excelSheetDiagnostic, error) {
	if db == nil {
		return nil, fmt.Errorf("duckdb connection is unavailable")
	}
	sheets, globalColumns, diagnostics, err := normalizeExcelWorkbook(filename)
	if err != nil {
		return diagnostics, err
	}

	csvPath, cleanup, err := writeNormalizedExcelCSV(sheets, globalColumns)
	if err != nil {
		return diagnostics, err
	}
	defer cleanup()

	createTableSQL := fmt.Sprintf(
		"CREATE TABLE \"%s\" AS SELECT * FROM read_csv_auto('%s', header=true, all_varchar=true)",
		tableName,
		sqlSingleQuoteEscape(csvPath),
	)
	if _, err := db.ExecContext(ctx, createTableSQL); err != nil {
		return diagnostics, err
	}
	return diagnostics, nil
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

type DataAnalysisInput struct {
	KnowledgeID string `json:"knowledge_id" jsonschema:"id of the knowledge to query"`
	Sql         string `json:"sql" jsonschema:"SQL to be executed on knowledge"`
}

type DataAnalysisTool struct {
	BaseTool
	knowledgeBaseService interfaces.KnowledgeBaseService
	knowledgeService     interfaces.KnowledgeService
	fileService          interfaces.FileService
	tenantService        interfaces.TenantService
	db                   *sql.DB
	sessionID            string
	createdTables        []string // Track tables created in this session
	queryCache           map[string]*types.ToolResult
	queryCacheMu         sync.Mutex
	// localBaseDir is the LOCAL_STORAGE_BASE_DIR value captured at construction
	// time so resolveFileServiceForKnowledge uses the same base path that was
	// used when the local FileService was initialised by DI.  Re-reading the
	// env var at request time can produce a different (or empty) value if the
	// variable was not exported to the sub-process or was set programmatically
	// after startup, causing GetFile to look in the wrong directory (#1040).
	localBaseDir string
}

func NewDataAnalysisTool(
	knowledgeBaseService interfaces.KnowledgeBaseService,
	knowledgeService interfaces.KnowledgeService,
	tenantService interfaces.TenantService,
	fileService interfaces.FileService,
	db *sql.DB,
	sessionID string,
) *DataAnalysisTool {
	return &DataAnalysisTool{
		BaseTool:             dataAnalysisTool,
		knowledgeBaseService: knowledgeBaseService,
		knowledgeService:     knowledgeService,
		fileService:          fileService,
		tenantService:        tenantService,
		db:                   db,
		sessionID:            sessionID,
		queryCache:           make(map[string]*types.ToolResult),
		// Capture LOCAL_STORAGE_BASE_DIR once at construction time so that every
		// call to resolveFileServiceForKnowledge uses the same base path.  The
		// env var is guaranteed to be set (or empty == "/data/files" fallback)
		// when the application starts and the DI container is assembled.
		localBaseDir: strings.TrimSpace(os.Getenv("LOCAL_STORAGE_BASE_DIR")),
	}
}

// recordCreatedTable records a table name for cleanup, ensuring uniqueness
// Returns true if the table was newly recorded, false if it already existed
func (t *DataAnalysisTool) recordCreatedTable(tableName string) bool {
	for _, name := range t.createdTables {
		if name == tableName {
			return false
		}
	}
	t.createdTables = append(t.createdTables, tableName)
	return true
}

// Cleanup cleans up the session-specific schema
func (t *DataAnalysisTool) Cleanup(ctx context.Context) {
	if len(t.createdTables) == 0 {
		logger.Infof(ctx, "[Tool][DataAnalysis] No tables to clean up for session: %s", t.sessionID)
		return
	}

	logger.Infof(ctx, "[Tool][DataAnalysis] Cleaning up %d tables for session: %s", len(t.createdTables), t.sessionID)

	for _, tableName := range t.createdTables {
		dropSQL := fmt.Sprintf("DROP TABLE IF EXISTS \"%s\"", tableName)
		if _, err := t.db.ExecContext(ctx, dropSQL); err != nil {
			logger.Errorf(ctx, "[Tool][DataAnalysis] Failed to drop table '%s': %v", tableName, err)
			// Continue to drop other tables even if one fails
			continue
		}
		logger.Infof(ctx, "[Tool][DataAnalysis] Successfully dropped table '%s'", tableName)
	}

	// Clear the list after cleanup
	t.createdTables = nil
}

// Execute executes the SQL query on DuckDB (only read-only queries are allowed)
func (t *DataAnalysisTool) Execute(ctx context.Context, args json.RawMessage) (*types.ToolResult, error) {
	logger.Infof(ctx, "[Tool][DataAnalysis] Execute started for session: %s", t.sessionID)
	var input DataAnalysisInput
	if err := json.Unmarshal(args, &input); err != nil {
		logger.Errorf(ctx, "[Tool][DataAnalysis] Failed to parse input args: %v", err)
		return &types.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("Failed to parse input args: %v", err),
		}, err
	}
	if cached := t.getCachedQueryResult(input); cached != nil {
		logger.Infof(ctx, "[Tool][DataAnalysis] Duplicate query hit cache for session %s", t.sessionID)
		return cached, nil
	}

	schema, err := t.LoadFromKnowledgeID(ctx, input.KnowledgeID)
	if err != nil {
		logger.Errorf(ctx, "[Tool][DataAnalysis] Failed to load knowledge ID '%s': %v", input.KnowledgeID, err)
		return &types.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("Failed to load knowledge ID '%s': %v", input.KnowledgeID, err),
		}, err
	}

	// Replace knowledge ID with table name
	input.Sql = strings.ReplaceAll(input.Sql, input.KnowledgeID, schema.TableName)
	if rewrittenSQL, fixes := reconcileSQLColumnsWithSchema(input.Sql, schema); len(fixes) > 0 {
		logger.Infof(ctx, "[Tool][DataAnalysis] Auto-rewrote SQL identifiers for session %s: %v", t.sessionID, fixes)
		input.Sql = rewrittenSQL
	}

	// Check if this is a read-only query
	normalizedSQL := strings.TrimSpace(strings.ToLower(input.Sql))
	isReadOnly := strings.HasPrefix(normalizedSQL, "select") ||
		strings.HasPrefix(normalizedSQL, "show") ||
		strings.HasPrefix(normalizedSQL, "describe") ||
		strings.HasPrefix(normalizedSQL, "explain") ||
		strings.HasPrefix(normalizedSQL, "pragma")

	if !isReadOnly {
		// Reject modification queries
		logger.Warnf(ctx, "[Tool][DataAnalysis] Modification query rejected for session %s: %s", t.sessionID, input.Sql)
		return &types.ToolResult{
			Success: false,
			Error:   "DuckDB tool only supports read-only queries (SELECT, SHOW, DESCRIBE, EXPLAIN, PRAGMA). Modification operations (INSERT, UPDATE, DELETE, CREATE, DROP, etc.) are not allowed.",
		}, fmt.Errorf("modification queries are not allowed")
	}

	// Validate SQL with comprehensive security checks
	// IMPORTANT: Must enable validateSelectStmt to block RangeFunction attacks
	_, validation := utils.ValidateSQL(input.Sql,
		utils.WithAllowedTables(schema.TableName),
		utils.WithSingleStatement(),      // Block multiple statements
		utils.WithNoDangerousFunctions(), // Block dangerous functions
	)
	if !validation.Valid {
		logger.Warnf(ctx, "[Tool][DataAnalysis] SQL validation failed for session %s: %v", t.sessionID, validation.Errors)
		return &types.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("SQL validation failed: %v", validation.Errors),
		}, fmt.Errorf("SQL validation failed: %v", validation.Errors)
	}

	logger.Infof(ctx, "[Tool][DataAnalysis] Received SQL query for session %s: %s", t.sessionID, input.Sql)
	// Execute single query and get results
	results, err := t.executeSingleQuery(ctx, input.Sql)
	if err != nil {
		if suggestion := buildMissingColumnSuggestion(err, schema); suggestion != "" {
			return &types.ToolResult{
				Success: false,
				Error:   fmt.Sprintf("Query execution failed: %v. %s", err, suggestion),
			}, err
		}
		return &types.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("Query execution failed: %v", err),
		}, err
	}

	queryOutput := t.formatQueryResults(results, input.Sql)
	if diagnostics := schema.excelDiagnosticsDescription(); diagnostics != "" {
		queryOutput += "\n" + diagnostics
	}
	logger.Infof(ctx, "[Tool][DataAnalysis] Completed execution query, total %d rows for session %s", len(results), t.sessionID)
	result := &types.ToolResult{
		Success: true,
		Output:  queryOutput,
		Data: map[string]interface{}{
			"rows":               results,
			"row_count":          len(results),
			"query":              input.Sql,
			"display_type":       ToolDataAnalysis,
			"session_id":         t.sessionID,
			"schema_diagnostics": schema.Metadata,
		},
	}
	t.storeQueryResult(input, result)
	return cloneToolResult(result), nil
}

func (t *DataAnalysisTool) queryCacheKey(input DataAnalysisInput) string {
	return strings.TrimSpace(input.KnowledgeID) + "\x00" + strings.Join(strings.Fields(input.Sql), " ")
}

func (t *DataAnalysisTool) getCachedQueryResult(input DataAnalysisInput) *types.ToolResult {
	t.queryCacheMu.Lock()
	defer t.queryCacheMu.Unlock()
	result := t.queryCache[t.queryCacheKey(input)]
	if result == nil {
		return nil
	}
	clone := cloneToolResult(result)
	clone.Output = "This exact data_analysis query has already been executed in this turn. " +
		"Use the cached result below to answer the user now; do not call data_analysis again with the same SQL.\n\n" +
		clone.Output
	return clone
}

func (t *DataAnalysisTool) storeQueryResult(input DataAnalysisInput, result *types.ToolResult) {
	t.queryCacheMu.Lock()
	defer t.queryCacheMu.Unlock()
	if t.queryCache == nil {
		t.queryCache = make(map[string]*types.ToolResult)
	}
	t.queryCache[t.queryCacheKey(input)] = cloneToolResult(result)
}

func cloneToolResult(result *types.ToolResult) *types.ToolResult {
	if result == nil {
		return nil
	}
	clone := *result
	if result.Data != nil {
		clone.Data = make(map[string]interface{}, len(result.Data))
		for k, v := range result.Data {
			clone.Data[k] = v
		}
	}
	if result.Images != nil {
		clone.Images = append([]string(nil), result.Images...)
	}
	return &clone
}

// executeSingleQuery executes a single SQL query and returns columns and results
// Parameters:
//   - ctx: context for cancellation and timeout
//   - sqlQuery: the SQL query to execute
//   - existingColumns: existing column names to merge with (can be nil or empty)
//
// Returns:
//   - []string: merged column names (existing + new columns, deduplicated)
//   - []map[string]string: query results
//   - error: any error that occurred during execution
func (t *DataAnalysisTool) executeSingleQuery(ctx context.Context, sqlQuery string) ([]map[string]string, error) {
	rows, err := t.db.QueryContext(ctx, sqlQuery)
	if err != nil {
		logger.Errorf(ctx, "[Tool][DataAnalysis] Query execution failed: %v", err)
		return nil, fmt.Errorf("query execution failed: %w", err)
	}
	defer rows.Close()

	// Get column names
	columns, err := rows.Columns()
	if err != nil {
		logger.Errorf(ctx, "[Tool][DataAnalysis] Failed to get columns: %v", err)
		return nil, fmt.Errorf("failed to get columns: %w", err)
	}

	// Process results
	results := make([]map[string]string, 0)
	for rows.Next() {
		columnValues := make([]interface{}, len(columns))
		columnPointers := make([]interface{}, len(columns))
		for i := range columnValues {
			columnPointers[i] = &columnValues[i]
		}

		if err := rows.Scan(columnPointers...); err != nil {
			logger.Errorf(ctx, "[Tool][DataAnalysis] Failed to scan row: %v", err)
			return nil, fmt.Errorf("failed to scan row: %w", err)
		}

		rowMap := make(map[string]string)
		for i, colName := range columns {
			val := columnValues[i]
			// Convert []byte to string for better readability
			if b, ok := val.([]byte); ok {
				rowMap[colName] = string(b)
			} else {
				rowMap[colName] = fmt.Sprintf("%v", val)
			}
		}
		results = append(results, rowMap)
	}

	if err := rows.Err(); err != nil {
		logger.Errorf(ctx, "[Tool][DataAnalysis] Error iterating rows: %v", err)
		return nil, fmt.Errorf("error iterating rows: %w", err)
	}

	return results, nil
}

// formatQueryResults formats query results into JSONL format (one JSON object per line)
func (t *DataAnalysisTool) formatQueryResults(results []map[string]string, query string) string {
	var output strings.Builder

	output.WriteString("=== DuckDB Query Results ===\n\n")
	output.WriteString(fmt.Sprintf("Executed SQL: %s\n\n", query))
	output.WriteString(fmt.Sprintf("Returned %d rows\n\n", len(results)))

	if len(results) == 0 {
		output.WriteString("No matching records found.\n")
		return output.String()
	}

	output.WriteString("=== Data Details ===\n\n")
	if len(results) > 10 {
		output.WriteString(fmt.Sprintf("Showing all %d records. Consider using a LIMIT clause to restrict the result count for better performance.\n\n", len(results)))
	}

	// Write each record as a separate JSON line
	for i, record := range results {
		recordBytes, _ := json.Marshal(record)

		// Remove the trailing newline added by Encode
		recordStr := strings.Trim(string(recordBytes), "\n")
		output.WriteString(fmt.Sprintf("record %d: %s\n", i+1, recordStr))
	}

	return output.String()
}

// TableSchema represents the schema information of a table
type TableSchema struct {
	TableName string                 `json:"table_name"`
	Columns   []ColumnInfo           `json:"columns"`
	RowCount  int64                  `json:"row_count"`
	Metadata  map[string]interface{} `json:"metadata,omitempty"`
}

// ColumnInfo represents information about a single column
type ColumnInfo struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Nullable string `json:"nullable"`
}

// LoadFromCSV loads data from a CSV file into a DuckDB table and returns the table schema
// Parameters:
//   - ctx: context for cancellation and timeout
//   - filename: path to the CSV file
//   - tableName: name of the table to create
//
// Returns:
//   - *TableSchema: schema information of the created table
//   - error: any error that occurred during the operation
func (t *DataAnalysisTool) LoadFromCSV(ctx context.Context, filename string, tableName string) (*TableSchema, error) {
	logger.Infof(ctx, "[Tool][DataAnalysis] Loading CSV file '%s' into table '%s' for session %s", filename, tableName, t.sessionID)

	// Record the created table for cleanup. If already exists, skip creation
	if t.recordCreatedTable(tableName) {
		// Create table from CSV using DuckDB's read_csv_auto function
		// with explicit header detection and VARCHAR coercion to align with
		// Excel loading behavior.
		// Table will be created in the session schema
		createTableSQL := fmt.Sprintf(
			"CREATE TABLE \"%s\" AS SELECT * FROM read_csv_auto('%s', header=true, all_varchar=true)",
			tableName, sqlSingleQuoteEscape(filename),
		)

		_, err := t.db.ExecContext(ctx, createTableSQL)
		if err != nil {
			logger.Errorf(ctx, "[Tool][DataAnalysis] Failed to create table from CSV: %v", err)
			return nil, fmt.Errorf("failed to create table from CSV: %w", err)
		}

		logger.Infof(ctx, "[Tool][DataAnalysis] Successfully created table '%s' from CSV file in session %s", tableName, t.sessionID)
	}

	// Get and return the table schema
	return t.LoadFromTable(ctx, tableName)
}

// LoadFromExcel loads data from an Excel file into a DuckDB table and returns the table schema.
//
// Multi-sheet .xlsx workbooks are normalized through excelize before loading:
// the tool infers each sheet's real header row, skips title/instruction rows,
// adds __sheet_name and __row_number, then loads a temporary CSV into DuckDB.
// If normalization is not possible (for example legacy .xls or ambiguous
// sheets), the tool falls back to the original DuckDB read_xlsx path.
//
// Parameters:
//   - ctx: context for cancellation and timeout
//   - filename: path to the Excel file
//   - tableName: name of the table to create
//
// Returns:
//   - *TableSchema: schema information of the created table
//   - error: any error that occurred during the operation
//
// Note: the normalized .xlsx path only requires DuckDB CSV support. The
// fallback path still requires DuckDB's excel/spatial extensions.
func (t *DataAnalysisTool) LoadFromExcel(ctx context.Context, filename string, tableName string) (*TableSchema, error) {
	logger.Infof(ctx, "[Tool][DataAnalysis] Loading Excel file '%s' into table '%s' for session %s", filename, tableName, t.sessionID)

	var diagnostics []excelSheetDiagnostic
	normalized := false

	// Record the created table for cleanup. If already exists, skip creation.
	if t.recordCreatedTable(tableName) {
		if smartDiagnostics, err := createNormalizedExcelTable(ctx, t.db, filename, tableName); err == nil {
			diagnostics = smartDiagnostics
			normalized = true
			logger.Infof(ctx,
				"[Tool][DataAnalysis] Successfully created normalized table '%s' from Excel file in session %s (sheets=%d)",
				tableName, t.sessionID, len(diagnostics),
			)
		} else {
			logger.Warnf(ctx,
				"[Tool][DataAnalysis] Smart Excel normalization failed for '%s' (session=%s): %v. Falling back to DuckDB read_xlsx.",
				filename, t.sessionID, err,
			)
			diagnostics = smartDiagnostics
			sheetNames, enumErr := t.listExcelSheets(ctx, filename)
			if enumErr != nil {
				logger.Warnf(ctx,
					"[Tool][DataAnalysis] Could not enumerate sheets for '%s' (session=%s): %v. Falling back to first sheet only.",
					filename, t.sessionID, enumErr,
				)
			}

			createTableSQL := buildExcelCreateTableSQL(tableName, filename, sheetNames)

			if _, err := t.db.ExecContext(ctx, createTableSQL); err != nil {
				logger.Errorf(ctx, "[Tool][DataAnalysis] Failed to create table from Excel (sheets=%v): %v", sheetNames, err)
				return nil, fmt.Errorf("failed to create table from Excel file (sheets=%v): %w", sheetNames, err)
			}

			logger.Infof(ctx,
				"[Tool][DataAnalysis] Successfully created fallback table '%s' from Excel file in session %s (sheets=%v)",
				tableName, t.sessionID, sheetNames,
			)
		}
	}

	schema, err := t.LoadFromTable(ctx, tableName)
	if err != nil {
		return nil, err
	}
	if schema.Metadata == nil {
		schema.Metadata = make(map[string]interface{})
	}
	if len(diagnostics) > 0 {
		schema.Metadata["excel_sheets"] = diagnostics
	}
	schema.Metadata["excel_normalized"] = normalized
	if !normalized {
		schema.Metadata["excel_normalization_warning"] = "Excel header inference was not used; schema may reflect the first row instead of the real header."
	}
	return schema, nil
}

// listExcelSheets returns the names of every sheet inside the given Excel workbook.
// The returned slice preserves the on-disk order of sheets.
//
// Prefer excelize so sheet enumeration does not depend on DuckDB's spatial
// extension. If excelize cannot open the file (for example legacy .xls), fall
// back to DuckDB's st_read_meta when available.
func (t *DataAnalysisTool) listExcelSheets(ctx context.Context, filename string) ([]string, error) {
	if f, err := excelize.OpenFile(filename); err == nil {
		defer func() { _ = f.Close() }()
		names := make([]string, 0, len(f.GetSheetList()))
		for _, name := range f.GetSheetList() {
			if strings.TrimSpace(name) == "" {
				continue
			}
			names = append(names, name)
		}
		if len(names) > 0 {
			return names, nil
		}
	} else {
		logger.Warnf(ctx, "[Tool][DataAnalysis] excelize could not enumerate sheets for '%s': %v", filename, err)
	}

	if t.db == nil {
		return nil, fmt.Errorf("failed to enumerate Excel sheets and DuckDB is unavailable")
	}

	metaSQL := fmt.Sprintf(
		"SELECT UNNEST(layers).name FROM st_read_meta('%s')",
		sqlSingleQuoteEscape(filename),
	)

	rows, err := t.db.QueryContext(ctx, metaSQL)
	if err != nil {
		return nil, fmt.Errorf("failed to query sheet metadata: %w", err)
	}
	defer rows.Close()

	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("failed to scan sheet name: %w", err)
		}
		if strings.TrimSpace(name) == "" {
			continue
		}
		names = append(names, name)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating sheet metadata rows: %w", err)
	}
	return names, nil
}

// buildExcelCreateTableSQL assembles the CREATE TABLE statement used by
// LoadFromExcel. Exposed at package level (lower-case) to make it trivially
// testable without a live DuckDB connection.
func buildExcelCreateTableSQL(tableName, filename string, sheetNames []string) string {
	escFile := sqlSingleQuoteEscape(filename)

	// No sheet info (enumeration failed or empty): read the first sheet only.
	if len(sheetNames) == 0 {
		return fmt.Sprintf(
			"CREATE TABLE \"%s\" AS SELECT * FROM read_xlsx('%s', header=true, all_varchar=true)",
			tableName, escFile,
		)
	}

	// Single sheet: keep it simple but still tag the source for consistency
	// with the multi-sheet path.
	if len(sheetNames) == 1 {
		escSheet := sqlSingleQuoteEscape(sheetNames[0])
		return fmt.Sprintf(
			"CREATE TABLE \"%s\" AS SELECT *, '%s' AS %s FROM read_xlsx('%s', sheet = '%s', header=true, all_varchar=true)",
			tableName, escSheet, excelSheetNameColumn, escFile, escSheet,
		)
	}

	// Multiple sheets: UNION ALL BY NAME tolerates schema differences
	// between sheets (missing columns become NULL, conflicting types are
	// widened).
	parts := make([]string, 0, len(sheetNames))
	for _, sheet := range sheetNames {
		escSheet := sqlSingleQuoteEscape(sheet)
		parts = append(parts, fmt.Sprintf(
			"SELECT *, '%s' AS %s FROM read_xlsx('%s', sheet = '%s', header=true, all_varchar=true)",
			escSheet, excelSheetNameColumn, escFile, escSheet,
		))
	}
	return fmt.Sprintf(
		"CREATE TABLE \"%s\" AS %s",
		tableName,
		strings.Join(parts, "\nUNION ALL BY NAME\n"),
	)
}

// LoadFromKnowledge loads data from a Knowledge entity into a DuckDB table and returns the table schema.
// It automatically determines the file type and calls the appropriate loading method.
//
// The source file is first materialized to a local temp file via FileService.GetFile
// so DuckDB's st_read / read_xlsx / read_csv_auto can open it directly. This
// side-steps provider-specific URL schemes (e.g. the local:// URL returned by
// the local file service) that DuckDB's extensions cannot resolve on their own.
//
// Parameters:
//   - ctx: context for cancellation and timeout
//   - knowledge: the Knowledge entity containing file information
//
// Returns:
//   - *TableSchema: schema information of the created table
//   - error: any error that occurred during the operation
func (t *DataAnalysisTool) LoadFromKnowledge(ctx context.Context, knowledge *types.Knowledge) (*TableSchema, error) {
	if knowledge == nil {
		return nil, fmt.Errorf("knowledge cannot be nil")
	}
	tableName := t.TableName(knowledge)

	// Normalize file type to lowercase for comparison
	fileType := strings.ToLower(knowledge.FileType)

	logger.Infof(ctx, "[Tool][DataAnalysis] Loading knowledge '%s' (type: %s) into table '%s' for session %s",
		knowledge.ID, fileType, tableName, t.sessionID)

	localPath, cleanup, err := t.materializeKnowledgeFile(ctx, knowledge)
	if err != nil {
		return nil, fmt.Errorf("failed to materialize knowledge '%s' for DuckDB: %w", knowledge.ID, err)
	}
	defer cleanup()

	switch fileType {
	case "csv":
		return t.LoadFromCSV(ctx, localPath, tableName)
	case "xlsx", "xls":
		return t.LoadFromExcel(ctx, localPath, tableName)
	default:
		logger.Warnf(ctx, "[Tool][DataAnalysis] Unsupported file type '%s' for knowledge '%s' in session %s",
			fileType, knowledge.ID, t.sessionID)
		return nil, fmt.Errorf("unsupported file type: %s (supported types: csv, xlsx, xls)", fileType)
	}
}

// materializeKnowledgeFile copies the knowledge's backing blob into a fresh
// temp file on the local filesystem so DuckDB can open it with ordinary path
// semantics. It returns the temp path and a cleanup closure that removes the
// temp file; the closure is always safe to call and is a no-op on failure.
//
// This hides storage-backend-specific URL schemes (local://, oss://, s3://,
// minio://, cos://, …) behind the FileService.GetFile abstraction, so the
// Data Analysis tool works identically across all deployments.
func (t *DataAnalysisTool) materializeKnowledgeFile(ctx context.Context, knowledge *types.Knowledge) (string, func(), error) {
	noop := func() {}

	reader, err := t.resolveFileServiceForKnowledge(ctx, knowledge).GetFile(ctx, knowledge.FilePath)
	if err != nil {
		return "", noop, fmt.Errorf("failed to open file for knowledge '%s': %w", knowledge.ID, err)
	}
	defer reader.Close()

	// Preserve the file extension so DuckDB's format auto-detection still
	// works (e.g. the CSV reader expects .csv, xlsx reader expects .xlsx).
	suffix := ""
	if ext := strings.ToLower(strings.TrimSpace(knowledge.FileType)); ext != "" {
		suffix = "." + ext
	}

	tmp, err := os.CreateTemp("", "weknora-data-analysis-*"+suffix)
	if err != nil {
		return "", noop, fmt.Errorf("failed to create temp file: %w", err)
	}
	tmpPath := tmp.Name()
	cleanup := func() {
		// Best-effort cleanup; a missing file is fine, any other error is
		// only logged to avoid masking the original operation's result.
		if err := os.Remove(tmpPath); err != nil && !os.IsNotExist(err) {
			logger.Warnf(ctx, "[Tool][DataAnalysis] Failed to remove temp file %s: %v", tmpPath, err)
		}
	}

	if _, err := io.Copy(tmp, reader); err != nil {
		_ = tmp.Close()
		cleanup()
		return "", noop, fmt.Errorf("failed to copy knowledge '%s' to temp file: %w", knowledge.ID, err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return "", noop, fmt.Errorf("failed to finalize temp file for knowledge '%s': %w", knowledge.ID, err)
	}

	logger.Infof(ctx, "[Tool][DataAnalysis] Materialized knowledge '%s' to temp file %s for session %s",
		knowledge.ID, tmpPath, t.sessionID)

	return tmpPath, cleanup, nil
}

// LoadFromKnowledgeID loads data from a Knowledge ID into a DuckDB table and returns the table schema
// Parameters:
//   - ctx: context for cancellation and timeout
//   - knowledgeID: the ID of the Knowledge entity
//
// Returns:
//   - string: the name of the created table
//   - *TableSchema: schema information of the created table
//   - error: any error that occurred during the operation
func (t *DataAnalysisTool) LoadFromKnowledgeID(ctx context.Context, knowledgeID string) (*TableSchema, error) {
	// Use GetKnowledgeByIDOnly to support cross-tenant shared KB
	knowledge, err := t.knowledgeService.GetKnowledgeByIDOnly(ctx, knowledgeID)
	if err != nil {
		logger.Errorf(ctx, "[Tool][DataAnalysis] Failed to get knowledge by ID '%s': %v", knowledgeID, err)
		return nil, fmt.Errorf("failed to get knowledge by ID: %w", err)
	}

	return t.LoadFromKnowledge(ctx, knowledge)
}

// LoadFromTable retrieves the schema information of an existing table
// Parameters:
//   - ctx: context for cancellation and timeout
//   - tableName: name of the table to query
//
// Returns:
//   - *TableSchema: schema information of the table
//   - error: any error that occurred during the operation
//
// Note: This function does NOT create the table, it only retrieves schema information
func (t *DataAnalysisTool) LoadFromTable(ctx context.Context, tableName string) (*TableSchema, error) {
	logger.Infof(ctx, "[Tool][DataAnalysis] Getting schema for table '%s' in session %s", tableName, t.sessionID)

	// Query to get column information using PRAGMA table_info or DESCRIBE
	schemaSQL := fmt.Sprintf("DESCRIBE \"%s\"", tableName)

	rows, err := t.db.QueryContext(ctx, schemaSQL)
	if err != nil {
		logger.Errorf(ctx, "[Tool][DataAnalysis] Failed to get table schema: %v", err)
		return nil, fmt.Errorf("failed to get table schema: %w", err)
	}
	defer rows.Close()

	// Parse column information
	columns := make([]ColumnInfo, 0)
	for rows.Next() {
		var colName, colType, nullable string
		var extra1, extra2, extra3 interface{} // DuckDB DESCRIBE may return additional columns

		// Try to scan with different column counts
		err := rows.Scan(&colName, &colType, &nullable, &extra1, &extra2, &extra3)
		if err != nil {
			// Try with fewer columns
			err = rows.Scan(&colName, &colType, &nullable)
			if err != nil {
				logger.Errorf(ctx, "[Tool][DataAnalysis] Failed to scan column info: %v", err)
				return nil, fmt.Errorf("failed to scan column info: %w", err)
			}
		}

		columns = append(columns, ColumnInfo{
			Name:     colName,
			Type:     colType,
			Nullable: nullable,
		})
	}

	if err := rows.Err(); err != nil {
		logger.Errorf(ctx, "[Tool][DataAnalysis] Error iterating schema rows: %v", err)
		return nil, fmt.Errorf("error iterating schema rows: %w", err)
	}

	// Get row count
	countSQL := fmt.Sprintf("SELECT COUNT(*) FROM \"%s\"", tableName)
	var rowCount int64
	if err := t.db.QueryRowContext(ctx, countSQL).Scan(&rowCount); err != nil {
		logger.Errorf(ctx, "[Tool][DataAnalysis] Failed to get row count: %v", err)
		return nil, fmt.Errorf("failed to get row count: %w", err)
	}

	schema := &TableSchema{
		TableName: tableName,
		Columns:   columns,
		RowCount:  rowCount,
		Metadata: map[string]interface{}{
			"column_count": len(columns),
			"session_id":   t.sessionID,
		},
	}

	logger.Infof(ctx, "[Tool][DataAnalysis] Retrieved schema for table '%s' in session %s: %d columns, %d rows",
		tableName, t.sessionID, len(columns), rowCount)

	return schema, nil
}

func (t *DataAnalysisTool) TableName(knowledge *types.Knowledge) string {
	return "k_" + strings.ReplaceAll(knowledge.ID, "-", "_")
}

// buildSchemaDescription builds a formatted schema description
func (t *TableSchema) Description() string {
	var builder strings.Builder
	builder.WriteString(fmt.Sprintf("Table name: %s\n", t.TableName))
	builder.WriteString(fmt.Sprintf("Columns: %d\n", len(t.Columns)))
	builder.WriteString(fmt.Sprintf("Rows: %d\n\n", t.RowCount))
	builder.WriteString("Column info:\n")

	for _, col := range t.Columns {
		builder.WriteString(fmt.Sprintf("- %s (%s)\n", col.Name, col.Type))
	}
	if diagnostics := t.excelDiagnosticsDescription(); diagnostics != "" {
		builder.WriteString("\n")
		builder.WriteString(diagnostics)
	}

	return builder.String()
}

func (t *TableSchema) excelDiagnosticsDescription() string {
	if t == nil || len(t.Metadata) == 0 {
		return ""
	}
	raw, ok := t.Metadata["excel_sheets"]
	if !ok {
		return ""
	}
	diagnostics, ok := raw.([]excelSheetDiagnostic)
	if !ok || len(diagnostics) == 0 {
		return ""
	}

	var builder strings.Builder
	builder.WriteString("Excel schema diagnostics:\n")
	if normalized, _ := t.Metadata["excel_normalized"].(bool); normalized {
		builder.WriteString("- Header rows were inferred automatically. Use these exact column names in SQL.\n")
	} else if warning, _ := t.Metadata["excel_normalization_warning"].(string); warning != "" {
		builder.WriteString("- ")
		builder.WriteString(warning)
		builder.WriteString("\n")
	}
	for _, diag := range diagnostics {
		if !diag.Normalized {
			if diag.SkipReason != "" {
				builder.WriteString(fmt.Sprintf("- Sheet %q skipped: %s\n", diag.Name, diag.SkipReason))
			}
			continue
		}
		builder.WriteString(fmt.Sprintf(
			"- Sheet %q: header row %d, data rows %d, columns: %s\n",
			diag.Name,
			diag.HeaderRow,
			diag.DataRows,
			strings.Join(diag.Columns, ", "),
		))
		for _, sample := range diag.SampleRows {
			builder.WriteString(fmt.Sprintf("  sample: %s\n", sample))
		}
	}
	return strings.TrimRight(builder.String(), "\n")
}

// resolveFileServiceForKnowledge resolves a provider-specific FileService based on the knowledge file path.
// It falls back to the injected default service when provider/config cannot be resolved.
func (t *DataAnalysisTool) resolveFileServiceForKnowledge(ctx context.Context, knowledge *types.Knowledge) interfaces.FileService {
	if knowledge == nil {
		logger.Warnf(ctx, "[Tool][DataAnalysis][storage] fallback default: session_id=%s reason=knowledge_nil", t.sessionID)
		return t.fileService
	}

	kbID := strings.TrimSpace(knowledge.KnowledgeBaseID)
	var kb *types.KnowledgeBase
	if t.knowledgeBaseService != nil && kbID != "" {
		var err error
		kb, err = t.knowledgeBaseService.GetKnowledgeBaseByID(ctx, kbID)
		if err != nil {
			logger.Warnf(ctx, "[Tool][DataAnalysis][storage] get kb failed, fallback default: session_id=%s knowledge_id=%s kb_id=%s err=%v",
				t.sessionID, knowledge.ID, kbID, err)
			return t.fileService
		}
	}
	if kb == nil && kbID != "" {
		logger.Infof(ctx, "[Tool][DataAnalysis][storage] kb not found, fallback default: session_id=%s knowledge_id=%s kb_id=%s",
			t.sessionID, knowledge.ID, kbID)
		return t.fileService
	}

	provider := ""
	if kb != nil {
		provider = kb.GetStorageProvider()
	}
	tenant, _ := ctx.Value(types.TenantInfoContextKey).(*types.Tenant)
	if tenant == nil {
		tenantID := uint64(0)
		if tid, ok := ctx.Value(types.TenantIDContextKey).(uint64); ok {
			tenantID = tid
		}
		if tenantID == 0 && kb != nil {
			tenantID = knowledge.TenantID
		}
		if tenantID > 0 && t.tenantService != nil {
			resolvedTenant, err := t.tenantService.GetTenantByID(ctx, tenantID)
			if err != nil {
				logger.Warnf(ctx, "[Tool][DataAnalysis][storage] get tenant failed: session_id=%s knowledge_id=%s kb_id=%s tenant_id=%d err=%v",
					t.sessionID, knowledge.ID, kbID, tenantID, err)
			} else if resolvedTenant != nil {
				tenant = resolvedTenant
				logger.Infof(ctx, "[Tool][DataAnalysis][storage] resolved tenant from service: session_id=%s knowledge_id=%s kb_id=%s tenant_id=%d",
					t.sessionID, knowledge.ID, kbID, tenantID)
			}
		}
	}
	if provider == "" && tenant != nil && tenant.StorageEngineConfig != nil {
		provider = strings.ToLower(strings.TrimSpace(tenant.StorageEngineConfig.DefaultProvider))
	}

	if provider == "" || tenant == nil || tenant.StorageEngineConfig == nil {
		hasTenantStorageConfig := tenant != nil && tenant.StorageEngineConfig != nil
		logger.Infof(ctx, "[Tool][DataAnalysis][storage] fallback default: session_id=%s knowledge_id=%s kb_id=%s provider=%q tenant_cfg=%t",
			t.sessionID, knowledge.ID, kbID, provider, hasTenantStorageConfig)
		return t.fileService
	}

	storageConfig := tenant.StorageEngineConfig
	// Use the localBaseDir captured at construction time rather than re-reading
	// LOCAL_STORAGE_BASE_DIR from os.Getenv here.  Reading the env var at
	// request-handling time can produce an empty string (or the wrong value)
	// when the variable was set programmatically before startup or is absent
	// from the process environment of the DI-constructed sub-component, causing
	// the newly created local FileService to use the /data/files fallback
	// instead of the configured path and therefore fail to locate files (#1040).
	baseDir := t.localBaseDir

	resolvedSvc, resolvedProvider, err := filesvc.NewFileServiceFromStorageConfig(provider, storageConfig, baseDir)
	if err != nil {
		logger.Warnf(ctx, "[Tool][DataAnalysis][storage] create file service failed, fallback default: session_id=%s knowledge_id=%s kb_id=%s provider=%s err=%v",
			t.sessionID, knowledge.ID, kbID, provider, err)
		return t.fileService
	}

	logger.Infof(ctx, "[Tool][DataAnalysis][storage] resolved file service: session_id=%s knowledge_id=%s kb_id=%s provider=%s",
		t.sessionID, knowledge.ID, kbID, resolvedProvider)
	return resolvedSvc
}
