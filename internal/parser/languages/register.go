package languages

import "github.com/zzet/gortex/internal/parser"

// RegisterAll registers all available language extractors.
func RegisterAll(reg *parser.Registry) {
	reg.Register(NewGoExtractor())
	reg.Register(NewTypeScriptExtractor())
	reg.Register(NewJavaScriptExtractor())
	reg.Register(NewPythonExtractor())
	reg.Register(NewRustExtractor())
	reg.Register(NewJavaExtractor())
	reg.Register(NewRubyExtractor())
	reg.Register(NewElixirExtractor())
	reg.Register(NewCExtractor())
	reg.Register(NewCppExtractor())
	reg.Register(NewHTMLExtractor())
	reg.Register(NewCSSExtractor())
}
