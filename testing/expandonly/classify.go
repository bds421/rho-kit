package expandonly

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	pg "github.com/pganalyze/pg_query_go/v6"
)

var (
	gooseUp   = regexp.MustCompile(`(?mi)^--\s*\+goose\s+up\b.*$`)
	gooseDown = regexp.MustCompile(`(?mi)^--\s*\+goose\s+down\b.*$`)
)

// upSection is a file's goose Up section and the line it starts on.
func upSection(src string) (string, int) {
	up, line := src, 1
	if loc := gooseUp.FindStringIndex(src); loc != nil {
		line += strings.Count(src[:loc[1]], "\n")
		up = src[loc[1]:]
	}
	if loc := gooseDown.FindStringIndex(up); loc != nil {
		up = up[:loc[0]]
	}
	return up, line
}

// indexesOf names every index a file's Up section creates, IF NOT EXISTS
// included, DO blocks read through.
func indexesOf(label, src string) (map[string]bool, error) {
	up, line := upSection(src)
	c := &classifier{label: label, created: map[string]bool{}, indexes: map[string]bool{}}
	if err := c.sql(up, line, -1); err != nil {
		return nil, err
	}
	return c.indexes, nil
}

// checkFile classifies the statements of a file's Up section; earlier names
// the indexes the set's earlier migrations create.
func checkFile(label, src string, earlier map[string]bool) ([]Finding, error) {
	own, err := indexesOf(label, src)
	if err != nil {
		return nil, err
	}
	up, line := upSection(src)
	c := &classifier{label: label, created: map[string]bool{}, indexes: map[string]bool{}, own: own, earlier: earlier}
	if err := c.sql(up, line, -1); err != nil {
		return nil, err
	}
	return c.findings, nil
}

type classifier struct {
	label   string
	created map[string]bool
	// indexes are the indexes the file creates; own those of the whole file,
	// earlier those of the set's earlier migrations.
	indexes  map[string]bool
	own      map[string]bool
	earlier  map[string]bool
	findings []Finding
}

// sql classifies the statements of text whose first line is line; a nested
// text (a DO block's) reports at the DO statement's line, given as at.
func (c *classifier) sql(text string, line, at int) error {
	tree, err := pg.Parse(text)
	if err != nil {
		return err
	}
	for _, raw := range tree.GetStmts() {
		start := int(raw.GetStmtLocation())
		end := len(text)
		if n := int(raw.GetStmtLen()); n > 0 {
			end = start + n
		}
		stmtLine := at
		if at < 0 {
			stmtLine = line + strings.Count(text[:start], "\n") + leadingLines(text[start:end])
		}
		reasons, nested := c.classify(raw.GetStmt())
		for _, r := range reasons {
			c.findings = append(c.findings, Finding{File: c.label, Line: stmtLine, Statement: quote(text[start:end]), Reason: r})
		}
		for _, n := range nested {
			if err := c.sql(n, line, stmtLine); err != nil {
				return err
			}
		}
	}
	return nil
}

// classify names what a statement takes from the previous release, and
// returns the SQL a DO block runs, to be classified in turn.
func (c *classifier) classify(n *pg.Node) (reasons, nested []string) {
	if c.create(n) {
		return c.uniqueIndex(n), nil
	}
	if s := n.GetDoStmt(); s != nil {
		return doBlock(s)
	}
	if t := n.GetAlterTableStmt(); t != nil {
		return c.alterTableStmt(t), nil
	}
	if r := c.onCreated(n); r != "" {
		return []string{r}, nil
	}
	if r := privileges(n); r != "" {
		return []string{r}, nil
	}
	return nil, nil
}

// create records what a CREATE makes; IF NOT EXISTS or OR REPLACE may meet
// an object the previous release uses, so it records nothing.
func (c *classifier) create(n *pg.Node) bool {
	switch s := n.GetNode().(type) {
	case *pg.Node_CreateStmt:
		if !s.CreateStmt.GetIfNotExists() {
			c.mark(relKey(s.CreateStmt.GetRelation()))
		}
	case *pg.Node_CreateTableAsStmt:
		if !s.CreateTableAsStmt.GetIfNotExists() {
			c.mark(relKey(s.CreateTableAsStmt.GetInto().GetRel()))
		}
	case *pg.Node_ViewStmt:
		if !s.ViewStmt.GetReplace() {
			c.mark(relKey(s.ViewStmt.GetView()))
		}
	case *pg.Node_CreateSeqStmt:
		if !s.CreateSeqStmt.GetIfNotExists() {
			c.mark(relKey(s.CreateSeqStmt.GetSequence()))
		}
	case *pg.Node_IndexStmt:
		i := s.IndexStmt
		key := ""
		if i.GetIdxname() != "" {
			key = "rel:" + schemaOr(i.GetRelation().GetSchemaname()) + "." + i.GetIdxname()
			c.indexes[key] = true
		}
		if !i.GetIfNotExists() {
			c.mark(key)
		}
	case *pg.Node_CreateFunctionStmt:
		if !s.CreateFunctionStmt.GetReplace() {
			c.mark(nameKey("func", s.CreateFunctionStmt.GetFuncname()))
		}
	case *pg.Node_CreateSchemaStmt:
		if !s.CreateSchemaStmt.GetIfNotExists() {
			c.mark(schemaKey(s.CreateSchemaStmt.GetSchemaname()))
		}
	default:
		return false
	}
	return true
}

func (c *classifier) uniqueIndex(n *pg.Node) []string {
	if i := n.GetIndexStmt(); i != nil && (i.GetUnique() || i.GetPrimary()) && !c.createdRel(i.GetRelation()) {
		return []string{"makes a unique index on an existing table, which the previous release's writes may break"}
	}
	return nil
}

// onCreated classifies the statements that change existing objects, which
// pass on objects the file created.
func (c *classifier) onCreated(n *pg.Node) string {
	switch s := n.GetNode().(type) {
	case *pg.Node_DropStmt:
		return c.drop(s.DropStmt)
	case *pg.Node_RenameStmt:
		if !c.createdRel(s.RenameStmt.GetRelation()) {
			return "renames what the previous release names"
		}
	case *pg.Node_AlterObjectSchemaStmt:
		if !c.createdRel(s.AlterObjectSchemaStmt.GetRelation()) {
			return "moves an object to another schema, where the previous release does not find it"
		}
	case *pg.Node_TruncateStmt:
		if !c.allRelsCreated(s.TruncateStmt.GetRelations()) {
			return "truncates a table the previous release reads"
		}
	case *pg.Node_CreatePolicyStmt:
		if !s.CreatePolicyStmt.GetPermissive() && !c.createdRel(s.CreatePolicyStmt.GetTable()) {
			return "adds a restrictive policy, which may deny the previous release"
		}
	case *pg.Node_AlterPolicyStmt:
		if !c.createdRel(s.AlterPolicyStmt.GetTable()) {
			return "changes a policy the previous release works under"
		}
	case *pg.Node_AlterOwnerStmt:
		o := s.AlterOwnerStmt
		if !c.createdRel(o.GetRelation()) && !c.created[objectKey(o.GetObjectType(), o.GetObject())] {
			return "changes the owner, which may take the previous release's role its rights"
		}
	case *pg.Node_GrantStmt:
		if g := s.GrantStmt; !g.GetIsGrant() && !c.allCreated(g.GetObjtype(), g.GetObjects()) {
			return "revokes a privilege the previous release may use"
		}
	}
	return ""
}

// privileges classifies the statements on types, roles, settings and
// databases, which a file does not create.
func privileges(n *pg.Node) string {
	switch s := n.GetNode().(type) {
	case *pg.Node_AlterDefaultPrivilegesStmt:
		if !s.AlterDefaultPrivilegesStmt.GetAction().GetIsGrant() {
			return "revokes a default privilege the previous release may rely on"
		}
	case *pg.Node_GrantRoleStmt:
		if !s.GrantRoleStmt.GetIsGrant() {
			return "revokes a role the previous release may act as"
		}
	case *pg.Node_AlterEnumStmt:
		if s.AlterEnumStmt.GetOldVal() != "" {
			return "renames an enum value the previous release uses"
		}
	case *pg.Node_AlterDomainStmt:
		if s.AlterDomainStmt.GetSubtype() != "N" {
			return "changes a domain the previous release's values must meet"
		}
	case *pg.Node_AlterRoleStmt, *pg.Node_DropRoleStmt, *pg.Node_DropOwnedStmt, *pg.Node_ReassignOwnedStmt:
		return "changes a role the previous release may connect or act as"
	case *pg.Node_AlterRoleSetStmt, *pg.Node_AlterDatabaseSetStmt:
		return "changes a setting the previous release runs under"
	case *pg.Node_DropdbStmt:
		return "drops a database the previous release may use"
	case *pg.Node_DropTableSpaceStmt:
		return "drops a tablespace the previous release may use"
	case *pg.Node_DropSubscriptionStmt:
		return "drops a subscription the previous release may use"
	case *pg.Node_DropUserMappingStmt:
		return "drops a user mapping the previous release may use"
	}
	return ""
}

func (c *classifier) alterTableStmt(s *pg.AlterTableStmt) (reasons []string) {
	if c.createdRel(s.GetRelation()) {
		return nil
	}
	for _, cmd := range s.GetCmds() {
		if r := alterTable(cmd.GetAlterTableCmd()); r != "" {
			reasons = append(reasons, r)
		}
	}
	return reasons
}

func (c *classifier) drop(s *pg.DropStmt) string {
	if c.allCreated(s.GetRemoveType(), s.GetObjects()) || c.retried(s) {
		return ""
	}
	kind := strings.ToLower(strings.ReplaceAll(strings.TrimPrefix(s.GetRemoveType().String(), "OBJECT_"), "_", " "))
	article := "a "
	if strings.ContainsRune("aeiou", rune(kind[0])) {
		article = "an "
	}
	return "drops " + article + kind + " the previous release may use"
}

// retried is DROP INDEX IF EXISTS of indexes the file creates and no earlier
// migration of the set does: what it can meet is only this file's own build
// that failed before (CREATE INDEX CONCURRENTLY leaves an invalid index),
// so a run again starts clean.
func (c *classifier) retried(s *pg.DropStmt) bool {
	if s.GetRemoveType() != pg.ObjectType_OBJECT_INDEX || !s.GetMissingOk() || len(s.GetObjects()) == 0 {
		return false
	}
	for _, o := range s.GetObjects() {
		key := objectKey(s.GetRemoveType(), o)
		if !c.own[key] || c.earlier[key] {
			return false
		}
	}
	return true
}

// expanding are the ALTER TABLE actions that leave the previous release
// working; every other one contracts.
var expanding = map[pg.AlterTableType]bool{
	pg.AlterTableType_AT_DropNotNull:        true,
	pg.AlterTableType_AT_SetStatistics:      true,
	pg.AlterTableType_AT_SetOptions:         true,
	pg.AlterTableType_AT_ResetOptions:       true,
	pg.AlterTableType_AT_SetStorage:         true,
	pg.AlterTableType_AT_SetCompression:     true,
	pg.AlterTableType_AT_ValidateConstraint: true,
	pg.AlterTableType_AT_ClusterOn:          true,
	pg.AlterTableType_AT_DropCluster:        true,
	pg.AlterTableType_AT_SetTableSpace:      true,
	pg.AlterTableType_AT_SetRelOptions:      true,
	pg.AlterTableType_AT_ResetRelOptions:    true,
	pg.AlterTableType_AT_ReplaceRelOptions:  true,
	pg.AlterTableType_AT_ReplicaIdentity:    true,
	pg.AlterTableType_AT_DisableRowSecurity: true,
	pg.AlterTableType_AT_NoForceRowSecurity: true,
	pg.AlterTableType_AT_GenericOptions:     true,
}

func alterTable(cmd *pg.AlterTableCmd) string {
	switch t := cmd.GetSubtype(); {
	case t == pg.AlterTableType_AT_AddColumn:
		return addColumn(cmd.GetDef().GetColumnDef())
	case t == pg.AlterTableType_AT_ColumnDefault:
		if cmd.GetDef() == nil || cmd.GetDef().GetAConst().GetIsnull() {
			return "drops the default of column " + cmd.GetName() + ", which the previous release's inserts may rely on"
		}
		return ""
	case t == pg.AlterTableType_AT_AddIndex:
		if idx := cmd.GetDef().GetIndexStmt(); idx != nil && !idx.GetUnique() && !idx.GetPrimary() {
			return ""
		}
		return "adds a unique index the previous release's writes may break"
	case expanding[t]:
		return ""
	case t == pg.AlterTableType_AT_DropColumn:
		return "drops column " + cmd.GetName() + ", which the previous release may use"
	case t == pg.AlterTableType_AT_AlterColumnType:
		return "changes the type of column " + cmd.GetName() + ", which the previous release reads and writes"
	case t == pg.AlterTableType_AT_SetNotNull:
		return "makes column " + cmd.GetName() + " NOT NULL, which the previous release's writes may break"
	case t == pg.AlterTableType_AT_AddConstraint:
		return "adds a constraint the previous release's writes may break"
	case t == pg.AlterTableType_AT_DropConstraint:
		return "drops constraint " + cmd.GetName() + ", which the previous release may rely on (ON CONFLICT, a CHECK, a foreign key)"
	case t == pg.AlterTableType_AT_ChangeOwner:
		return "changes the owner, which may take the previous release's role its rights"
	case t == pg.AlterTableType_AT_EnableRowSecurity, t == pg.AlterTableType_AT_ForceRowSecurity:
		return "turns on row-level security, which may deny the previous release"
	default:
		return "alters the table (" + strings.TrimPrefix(t.String(), "AT_") + ") in a way the previous release may not work with"
	}
}

// addColumn: a new column the previous release does not name must not stop
// its inserts, so NOT NULL needs a default (an identity or generated column
// fills itself), and a unique one no default (every such insert would take
// the same value); a CHECK passes the NULL those inserts leave.
func addColumn(col *pg.ColumnDef) string {
	notNull, defaulted := col.GetIsNotNull(), col.GetRawDefault() != nil
	filled, unique := col.GetIdentity() != "" || col.GetGenerated() != "", false
	for _, n := range col.GetConstraints() {
		switch n.GetConstraint().GetContype() {
		case pg.ConstrType_CONSTR_PRIMARY:
			notNull, unique = true, true
		case pg.ConstrType_CONSTR_UNIQUE:
			unique = true
		case pg.ConstrType_CONSTR_NOTNULL:
			notNull = true
		case pg.ConstrType_CONSTR_DEFAULT:
			defaulted = true
		case pg.ConstrType_CONSTR_IDENTITY, pg.ConstrType_CONSTR_GENERATED:
			filled = true
		}
	}
	switch {
	case unique && defaulted:
		return "adds column " + col.GetColname() + " unique with a default, which every insert of the previous release takes alike"
	case notNull && !filled && !defaulted:
		return "adds column " + col.GetColname() + " NOT NULL without a default, which the previous release's inserts do not fill"
	}
	return ""
}

// doBlock returns the SQL a PL/pgSQL DO block runs, from its parse tree; a
// dynamic EXECUTE or another language cannot be checked.
func doBlock(s *pg.DoStmt) (reasons, nested []string) {
	body, language := "", "plpgsql"
	for _, a := range s.GetArgs() {
		d := a.GetDefElem()
		switch d.GetDefname() {
		case "as":
			body = d.GetArg().GetString_().GetSval()
		case "language":
			language = d.GetArg().GetString_().GetSval()
		}
	}
	if language != "plpgsql" {
		return []string{"runs a DO block in " + language + ", which cannot be checked"}, nil
	}
	tag := "$expandonly$"
	if strings.Contains(body, tag) {
		return []string{"runs a DO block that cannot be checked"}, nil
	}
	out, err := pg.ParsePlPgSqlToJSON("CREATE FUNCTION expandonly_do() RETURNS void LANGUAGE plpgsql AS " + tag + body + tag)
	if err != nil {
		return []string{fmt.Sprintf("runs a DO block that does not parse (%v)", err)}, nil
	}
	var tree any
	if err := json.Unmarshal([]byte(out), &tree); err != nil {
		return []string{"runs a DO block that cannot be checked"}, nil
	}
	walk(tree, func(key string, v map[string]any) {
		switch key {
		case "PLpgSQL_stmt_execsql":
			if q, ok := query(v["sqlstmt"]); ok {
				nested = append(nested, q)
			}
		case "PLpgSQL_stmt_dynexecute":
			reasons = append(reasons, "runs dynamic SQL (EXECUTE), which cannot be checked")
		}
	})
	return reasons, nested
}

func query(v any) (string, bool) {
	m, ok := v.(map[string]any)
	if !ok {
		return "", false
	}
	e, ok := m["PLpgSQL_expr"].(map[string]any)
	if !ok {
		return "", false
	}
	q, ok := e["query"].(string)
	return q, ok
}

func walk(v any, visit func(string, map[string]any)) {
	switch t := v.(type) {
	case map[string]any:
		for k, child := range t {
			if m, ok := child.(map[string]any); ok {
				visit(k, m)
			}
			walk(child, visit)
		}
	case []any:
		for _, child := range t {
			walk(child, visit)
		}
	}
}

// leadingComments is the length of the blank lines and comments before a
// statement's text.
func leadingComments(stmt string) int {
	rest := stmt
	for {
		t := strings.TrimLeft(rest, " \t\r\n")
		var end int
		switch {
		case strings.HasPrefix(t, "--"):
			if end = strings.IndexByte(t, '\n'); end >= 0 {
				end++
			}
		case strings.HasPrefix(t, "/*"):
			if end = strings.Index(t, "*/"); end >= 0 {
				end += 2
			}
		default:
			return len(stmt) - len(t)
		}
		if end < 0 {
			return len(stmt) - len(t)
		}
		rest = t[end:]
	}
}

// leadingLines counts the lines before a statement's text.
func leadingLines(stmt string) int { return strings.Count(stmt[:leadingComments(stmt)], "\n") }

func quote(stmt string) string {
	s := strings.Join(strings.Fields(stripComments(stmt[leadingComments(stmt):])), " ")
	if len(s) > 80 {
		s = s[:77] + "..."
	}
	return "`" + s + "`"
}

func stripComments(stmt string) string {
	var b strings.Builder
	for _, l := range strings.Split(stmt, "\n") {
		if t := strings.TrimSpace(l); strings.HasPrefix(t, "--") {
			continue
		}
		b.WriteString(l)
		b.WriteByte('\n')
	}
	return b.String()
}
