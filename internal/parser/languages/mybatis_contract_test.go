package languages

import (
	"testing"

	"github.com/zzet/gortex/internal/graph"
)

// TestMyBatisStatementContract pins the full statement-kind matrix and the
// per-statement node/edge shape (qualified name, sql-kind, placeholder call
// edge) so a regression in any one statement kind fails here.
func TestMyBatisStatementContract(t *testing.T) {
	const mapper = `<?xml version="1.0" encoding="UTF-8"?>
<mapper namespace="com.app.OrderMapper">
  <select id="get" resultType="Order">SELECT 1</select>
  <insert id="create">INSERT INTO o VALUES (1)</insert>
  <update id="touch">UPDATE o SET x = 1</update>
  <delete id="remove">DELETE FROM o</delete>
</mapper>
`
	res, err := NewMyBatisExtractor().Extract("OrderMapper.xml", []byte(mapper))
	if err != nil {
		t.Fatal(err)
	}

	stmts := map[string]*graph.Node{}
	for _, n := range res.Nodes {
		if n.Kind == graph.KindMethod {
			stmts[n.Name] = n
		}
	}
	calls := map[string]bool{}
	for _, e := range res.Edges {
		if e.Kind == graph.EdgeCalls {
			calls[e.From+" -> "+e.To] = true
		}
	}

	cases := []struct{ id, kind string }{
		{"get", "select"},
		{"create", "insert"},
		{"touch", "update"},
		{"remove", "delete"},
	}
	for _, c := range cases {
		s := stmts[c.id]
		if s == nil {
			t.Errorf("statement %q (%s) was not extracted", c.id, c.kind)
			continue
		}
		if s.QualName != "com.app.OrderMapper."+c.id {
			t.Errorf("%s QualName = %q, want com.app.OrderMapper.%s", c.id, s.QualName, c.id)
		}
		if s.Meta["mybatis_sql_kind"] != c.kind {
			t.Errorf("%s sql_kind = %v, want %s", c.id, s.Meta["mybatis_sql_kind"], c.kind)
		}
		want := "com.app.OrderMapper::" + c.id + " -> unresolved::mybatis::com.app.OrderMapper::" + c.id
		if !calls[want] {
			t.Errorf("missing placeholder call edge for %q (%s)", c.id, c.kind)
		}
	}
}
