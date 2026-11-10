package expandonly

import pg "github.com/pganalyze/pg_query_go/v6"

// Objects a file created are keyed by kind and schema-qualified name, an
// unqualified name in public: tables, indexes, views and sequences share a
// namespace (rel), functions and schemas have their own. "" is no object.

func schemaOr(s string) string {
	if s == "" {
		return "public"
	}
	return s
}

func relKey(rv *pg.RangeVar) string {
	if rv.GetRelname() == "" {
		return ""
	}
	return "rel:" + schemaOr(rv.GetSchemaname()) + "." + rv.GetRelname()
}

// nameKey keys a qualified name given as its parts.
func nameKey(kind string, items []*pg.Node) string {
	n := len(items)
	if n == 0 || items[n-1].GetString_().GetSval() == "" {
		return ""
	}
	schema := ""
	if n >= 2 {
		schema = items[n-2].GetString_().GetSval()
	}
	return kind + ":" + schemaOr(schema) + "." + items[n-1].GetString_().GetSval()
}

func schemaKey(name string) string {
	if name == "" {
		return ""
	}
	return "schema:" + name
}

// objectKey keys an object of a DROP, GRANT or REVOKE of type t.
func objectKey(t pg.ObjectType, o *pg.Node) string {
	switch t {
	case pg.ObjectType_OBJECT_TABLE, pg.ObjectType_OBJECT_INDEX, pg.ObjectType_OBJECT_VIEW,
		pg.ObjectType_OBJECT_MATVIEW, pg.ObjectType_OBJECT_SEQUENCE:
		if rv := o.GetRangeVar(); rv != nil {
			return relKey(rv)
		}
		return nameKey("rel", o.GetList().GetItems())
	case pg.ObjectType_OBJECT_FUNCTION, pg.ObjectType_OBJECT_PROCEDURE, pg.ObjectType_OBJECT_ROUTINE:
		return nameKey("func", o.GetObjectWithArgs().GetObjname())
	case pg.ObjectType_OBJECT_SCHEMA:
		return schemaKey(o.GetString_().GetSval())
	}
	return ""
}

func (c *classifier) mark(key string) {
	if key != "" {
		c.created[key] = true
	}
}

func (c *classifier) createdRel(rv *pg.RangeVar) bool { return c.created[relKey(rv)] }

// allCreated reports whether the file created every object of type t.
func (c *classifier) allCreated(t pg.ObjectType, objects []*pg.Node) bool {
	for _, o := range objects {
		if !c.created[objectKey(t, o)] {
			return false
		}
	}
	return len(objects) > 0
}

func (c *classifier) allRelsCreated(rels []*pg.Node) bool {
	for _, r := range rels {
		if !c.createdRel(r.GetRangeVar()) {
			return false
		}
	}
	return len(rels) > 0
}
