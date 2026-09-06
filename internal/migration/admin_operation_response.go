package migration

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"

	"gorm.io/gorm"
)

var ErrAdminOperationResponseSchema = errors.New("admin operation response schema mismatch or unavailable")

func adminOperationResponseTableContract() businessGroupTableContract {
	return businessGroupTableContract{
		name: "admin_operation_responses",
		columns: []businessGroupColumnContract{
			{name: "id", columnType: "bigint", nullable: "NO", extra: "auto_increment"},
			{name: "guid", columnType: "bigint", nullable: "NO"},
			{name: "created_at", columnType: "bigint", nullable: "NO"},
			{name: "created_by", columnType: "bigint", nullable: "YES"},
			{name: "updated_at", columnType: "bigint", nullable: "NO"},
			{name: "updated_by", columnType: "bigint", nullable: "YES"},
			{name: "is_deleted", columnType: "int", nullable: "NO", defaultVal: sql.NullString{String: "0", Valid: true}},
			{name: "operation_id", columnType: "bigint", nullable: "NO"},
			{name: "http_status", columnType: "int", nullable: "NO"},
			{name: "media_type", columnType: "varchar(64)", nullable: "NO", characterSet: "ascii", collation: "ascii_bin"},
			{name: "response_body", columnType: "varbinary(4096)", nullable: "NO"},
			{name: "body_sha256", columnType: "char(64)", nullable: "NO", characterSet: "ascii", collation: "ascii_bin"},
		},
		indexes: []businessGroupIndexContract{
			{name: "PRIMARY", columns: []string{"id"}, unique: true},
			{name: "uk_admin_operation_responses_guid", columns: []string{"guid"}, unique: true},
			{name: "uk_admin_operation_responses_operation", columns: []string{"operation_id"}, unique: true},
			{name: "idx_admin_operation_responses_active", columns: []string{"is_deleted", "created_at"}, unique: false},
		},
		checks: []businessGroupCheckContract{
			{name: "chk_admin_operation_responses_http", clause: "http_status = 201 AND media_type = 'application/json'", enforced: "YES"},
			{name: "chk_admin_operation_responses_body", clause: "OCTET_LENGTH(response_body) BETWEEN 2 AND 4096 AND OCTET_LENGTH(body_sha256) = 64", enforced: "YES"},
			{name: "chk_admin_operation_responses_immutable", clause: "created_at >= 0 AND updated_at = created_at AND is_deleted = 0 AND ((created_by IS NULL AND updated_by IS NULL) OR (created_by IS NOT NULL AND updated_by = created_by))", enforced: "YES"},
		},
	}
}

// VerifyAdminOperationResponseSchema rejects partial or drifted snapshot
// tables before the application exposes managed action routes.
func VerifyAdminOperationResponseSchema(ctx context.Context, db *gorm.DB) error {
	if db == nil {
		return ErrAdminOperationResponseSchema
	}
	var currentSchema string
	if err := db.WithContext(ctx).Raw("SELECT DATABASE()").Row().Scan(&currentSchema); err != nil || currentSchema == "" {
		return ErrAdminOperationResponseSchema
	}
	contract := adminOperationResponseTableContract()
	metadata, ok := loadBusinessGroupTableMetadata(ctx, db, contract.name)
	if !ok || !matchesAdminOperationResponseContract(contract, metadata, currentSchema) {
		return ErrAdminOperationResponseSchema
	}
	return nil
}

func matchesAdminOperationResponseContract(want businessGroupTableContract, got businessGroupTableMetadata, currentSchema string) bool {
	if got.engine != "InnoDB" || got.characterSet != "utf8mb4" || got.collation != "utf8mb4_unicode_ci" || len(got.columns) != len(want.columns) {
		return false
	}
	for i, expected := range want.columns {
		actual := got.columns[i]
		columnType := strings.ToLower(actual.columnType)
		if actual.name != expected.name || columnType != expected.columnType || strings.Contains(columnType, "unsigned") || actual.nullable != expected.nullable ||
			actual.defaultVal != expected.defaultVal || strings.ToLower(actual.extra) != expected.extra || actual.characterSet != expected.characterSet || actual.collation != expected.collation {
			return false
		}
	}
	indexes := make(map[string][]businessGroupIndexMetadata, len(want.indexes))
	for _, index := range got.indexes {
		indexes[index.name] = append(indexes[index.name], index)
	}
	if len(indexes) != len(want.indexes) {
		return false
	}
	for _, expected := range want.indexes {
		rows := indexes[expected.name]
		if len(rows) != len(expected.columns) {
			return false
		}
		for i, row := range rows {
			if row.sequence != i+1 || row.column != expected.columns[i] || (row.nonUnique == 0) != expected.unique || !validRequiredBusinessGroupIndexMetadata(row) {
				return false
			}
		}
	}
	if len(got.foreignKeys) != 1 {
		return false
	}
	fk := got.foreignKeys[0]
	if fk.name != "fk_admin_operation_responses_operation" || fk.column != "operation_id" || fk.ordinal != 1 || fk.targetSchema != currentSchema ||
		fk.targetTable != "admin_operations" || fk.targetColumn != "id" || !restrictRule(fk.deleteRule) || !restrictRule(fk.updateRule) {
		return false
	}
	checks := make(map[string][]businessGroupCheckMetadata, len(want.checks))
	for _, check := range got.checks {
		checks[check.name] = append(checks[check.name], check)
	}
	if len(checks) != len(want.checks) {
		return false
	}
	for _, expected := range want.checks {
		rows := checks[expected.name]
		if len(rows) != 1 || rows[0].enforced != "YES" {
			return false
		}
		wantClause, wantOK := canonicalizeAdminOperationResponseCheck(expected.clause)
		gotClause, gotOK := canonicalizeAdminOperationResponseCheck(rows[0].clause)
		if !wantOK || !gotOK || gotClause != wantClause {
			return false
		}
	}
	return true
}

type adminOperationResponseCheckToken struct {
	kind byte
	text string
}

type adminOperationResponseCheckNode struct {
	kind     string
	atom     string
	children []adminOperationResponseCheckNode
}

// canonicalizeAdminOperationResponseCheck preserves boolean operator
// boundaries while accepting MySQL's presentation-only metadata changes.
func canonicalizeAdminOperationResponseCheck(clause string) (string, bool) {
	tokens, ok := tokenizeAdminOperationResponseCheck(clause)
	if !ok || len(tokens) == 0 {
		return "", false
	}
	parser := adminOperationResponseCheckParser{tokens: tokens}
	node, ok := parser.parseOr()
	if !ok || parser.pos != len(tokens) {
		return "", false
	}
	return node.canonical(), true
}

func tokenizeAdminOperationResponseCheck(clause string) ([]adminOperationResponseCheckToken, bool) {
	// MySQL exposes quote delimiters in CHECK_CLAUSE as \' on some 8.x
	// builds. The response contract has no quote characters inside literals.
	clause = strings.ReplaceAll(clause, `\'`, `'`)
	var tokens []adminOperationResponseCheckToken
	for pos := 0; pos < len(clause); {
		if isCheckSpace(clause[pos]) {
			pos++
			continue
		}
		switch clause[pos] {
		case '`':
			end := pos + 1
			for end < len(clause) && clause[end] != '`' {
				end++
			}
			if end == len(clause) || !isCheckIdentifier(clause[pos+1:end]) {
				return nil, false
			}
			tokens = append(tokens, adminOperationResponseCheckToken{kind: 'i', text: strings.ToLower(clause[pos+1 : end])})
			pos = end + 1
			continue
		case '\'':
			end := pos + 1
			var value strings.Builder
			closed := false
			for end < len(clause) {
				if clause[end] == '\'' {
					if end+1 < len(clause) && clause[end+1] == '\'' {
						value.WriteByte('\'')
						end += 2
						continue
					}
					closed = true
					end++
					break
				}
				if clause[end] < 0x20 || clause[end] > 0x7e || clause[end] == '\\' {
					return nil, false
				}
				value.WriteByte(clause[end])
				end++
			}
			if !closed {
				return nil, false
			}
			tokens = append(tokens, adminOperationResponseCheckToken{kind: 's', text: value.String()})
			pos = end
			continue
		case '(', ')', ',':
			tokens = append(tokens, adminOperationResponseCheckToken{kind: clause[pos], text: clause[pos : pos+1]})
			pos++
			continue
		}
		if isASCIIAlpha(clause[pos]) || clause[pos] == '_' {
			end := pos + 1
			for end < len(clause) && (isASCIIAlpha(clause[end]) || isASCIIDigit(clause[end]) || clause[end] == '_') {
				end++
			}
			tokens = append(tokens, adminOperationResponseCheckToken{kind: 'i', text: strings.ToLower(clause[pos:end])})
			pos = end
			continue
		}
		if isASCIIDigit(clause[pos]) {
			end := pos + 1
			for end < len(clause) && isASCIIDigit(clause[end]) {
				end++
			}
			tokens = append(tokens, adminOperationResponseCheckToken{kind: 'n', text: clause[pos:end]})
			pos = end
			continue
		}
		if strings.ContainsRune("=><", rune(clause[pos])) {
			end := pos + 1
			if end < len(clause) && clause[end] == '=' {
				end++
			}
			op := clause[pos:end]
			if op != "=" && op != ">=" && op != "<=" && op != ">" && op != "<" {
				return nil, false
			}
			tokens = append(tokens, adminOperationResponseCheckToken{kind: 'o', text: op})
			pos = end
			continue
		}
		return nil, false
	}
	return tokens, true
}

type adminOperationResponseCheckParser struct {
	tokens []adminOperationResponseCheckToken
	pos    int
}

func (p *adminOperationResponseCheckParser) parseOr() (adminOperationResponseCheckNode, bool) {
	left, ok := p.parseAnd()
	if !ok {
		return adminOperationResponseCheckNode{}, false
	}
	for p.matchWord("or") {
		right, ok := p.parseAnd()
		if !ok {
			return adminOperationResponseCheckNode{}, false
		}
		left = mergeAdminOperationResponseBoolean("or", left, right)
	}
	return left, true
}

func (p *adminOperationResponseCheckParser) parseAnd() (adminOperationResponseCheckNode, bool) {
	left, ok := p.parseNot()
	if !ok {
		return adminOperationResponseCheckNode{}, false
	}
	for p.matchWord("and") {
		right, ok := p.parseNot()
		if !ok {
			return adminOperationResponseCheckNode{}, false
		}
		left = mergeAdminOperationResponseBoolean("and", left, right)
	}
	return left, true
}

func (p *adminOperationResponseCheckParser) parseNot() (adminOperationResponseCheckNode, bool) {
	if p.matchWord("not") {
		child, ok := p.parseNot()
		if !ok {
			return adminOperationResponseCheckNode{}, false
		}
		return adminOperationResponseCheckNode{kind: "not", children: []adminOperationResponseCheckNode{child}}, true
	}
	return p.parsePrimary()
}

func (p *adminOperationResponseCheckParser) parsePrimary() (adminOperationResponseCheckNode, bool) {
	if p.matchKind('(') {
		node, ok := p.parseOr()
		if !ok || !p.matchKind(')') {
			return adminOperationResponseCheckNode{}, false
		}
		return node, true
	}
	left, ok := p.parseScalar()
	if !ok {
		return adminOperationResponseCheckNode{}, false
	}
	if p.matchWord("is") {
		negated := p.matchWord("not")
		if !p.matchWord("null") {
			return adminOperationResponseCheckNode{}, false
		}
		operator := "isnull"
		if negated {
			operator = "notnull"
		}
		return adminOperationResponseCheckNode{kind: "atom", atom: operator + "(" + left + ")"}, true
	}
	if p.matchWord("between") {
		low, lowOK := p.parseScalar()
		if !lowOK || !p.matchWord("and") {
			return adminOperationResponseCheckNode{}, false
		}
		high, highOK := p.parseScalar()
		if !highOK {
			return adminOperationResponseCheckNode{}, false
		}
		return adminOperationResponseCheckNode{kind: "atom", atom: "between(" + left + "," + low + "," + high + ")"}, true
	}
	if p.pos >= len(p.tokens) || p.tokens[p.pos].kind != 'o' {
		return adminOperationResponseCheckNode{}, false
	}
	operator := p.tokens[p.pos].text
	p.pos++
	right, ok := p.parseScalar()
	if !ok {
		return adminOperationResponseCheckNode{}, false
	}
	return adminOperationResponseCheckNode{kind: "atom", atom: "compare(" + operator + "," + left + "," + right + ")"}, true
}

func (p *adminOperationResponseCheckParser) parseScalar() (string, bool) {
	if p.matchKind('(') {
		value, ok := p.parseScalar()
		if !ok || !p.matchKind(')') {
			return "", false
		}
		return value, true
	}
	if p.pos >= len(p.tokens) {
		return "", false
	}
	token := p.tokens[p.pos]
	switch token.kind {
	case 'n':
		p.pos++
		value := strings.TrimLeft(token.text, "0")
		if value == "" {
			value = "0"
		}
		return "number(" + value + ")", true
	case 's':
		p.pos++
		return "string(" + strconv.Quote(token.text) + ")", true
	case 'i':
		p.pos++
		if token.text == "_utf8mb4" {
			if p.pos >= len(p.tokens) || p.tokens[p.pos].kind != 's' {
				return "", false
			}
			literal := p.tokens[p.pos].text
			p.pos++
			return "string(" + strconv.Quote(literal) + ")", true
		}
		if !p.matchKind('(') {
			return "identifier(" + token.text + ")", true
		}
		function := token.text
		if function != "length" && function != "octet_length" {
			return "", false
		}
		if p.pos >= len(p.tokens) || p.tokens[p.pos].kind != 'i' || p.tokens[p.pos].text == "_utf8mb4" {
			return "", false
		}
		argument := p.tokens[p.pos].text
		p.pos++
		if !p.matchKind(')') {
			return "", false
		}
		return "call(length,identifier(" + argument + "))", true
	default:
		return "", false
	}
}

func (p *adminOperationResponseCheckParser) matchWord(word string) bool {
	if p.pos >= len(p.tokens) || p.tokens[p.pos].kind != 'i' || p.tokens[p.pos].text != word {
		return false
	}
	p.pos++
	return true
}

func (p *adminOperationResponseCheckParser) matchKind(kind byte) bool {
	if p.pos >= len(p.tokens) || p.tokens[p.pos].kind != kind {
		return false
	}
	p.pos++
	return true
}

func mergeAdminOperationResponseBoolean(kind string, left, right adminOperationResponseCheckNode) adminOperationResponseCheckNode {
	children := make([]adminOperationResponseCheckNode, 0, 4)
	if left.kind == kind {
		children = append(children, left.children...)
	} else {
		children = append(children, left)
	}
	if right.kind == kind {
		children = append(children, right.children...)
	} else {
		children = append(children, right)
	}
	return adminOperationResponseCheckNode{kind: kind, children: children}
}

func (node adminOperationResponseCheckNode) canonical() string {
	if node.kind == "atom" {
		return node.atom
	}
	children := make([]string, len(node.children))
	for index := range node.children {
		children[index] = node.children[index].canonical()
	}
	return node.kind + "(" + strings.Join(children, ",") + ")"
}
