package languages

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zzet/gortex/internal/graph"
)

func TestPHPExtractor_FactoryChainReceiver(t *testing.T) {
	src := []byte("<?php\n" +
		"function builder() { return new Widget(); }\n" +
		"function run() {\n" +
		"    builder()->withX()->build();\n" +
		"}\n")
	res, err := NewPHPExtractor().Extract("w.php", src)
	require.NoError(t, err)

	var build *graph.Edge
	for _, e := range res.Edges {
		if e.Kind == graph.EdgeCalls && e.To == "unresolved::*.build" {
			build = e
		}
	}
	require.NotNil(t, build, "build() call edge")
	if got, _ := build.Meta["receiver_expr"].(string); got != "builder().withX()" {
		t.Errorf("receiver_expr = %q, want builder().withX()", got)
	}
}
