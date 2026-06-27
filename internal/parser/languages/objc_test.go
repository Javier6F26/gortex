package languages

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zzet/gortex/internal/graph"
)

func TestObjCExtractor_Basics(t *testing.T) {
	src := []byte(`#import <Foundation/Foundation.h>
#import "Helper.h"

@interface Greeter : NSObject
- (void)greet:(NSString *)name;
+ (instancetype)sharedInstance;
@end

@implementation Greeter
- (void)greet:(NSString *)name {
    NSLog(@"hi %@", name);
}
+ (instancetype)sharedInstance {
    return nil;
}
@end

static int helper(int x) {
    return x + 1;
}
`)
	e := NewObjCExtractor()
	require.Equal(t, "objc", e.Language())

	res, err := e.Extract("greet.m", src)
	require.NoError(t, err)

	var gotGreeter, gotGreetSel, gotShared, gotHelper bool
	for _, n := range res.Nodes {
		switch n.Name {
		case "Greeter":
			gotGreeter = true
		case "greet:":
			gotGreetSel = true
		case "sharedInstance":
			gotShared = true
		case "helper":
			gotHelper = true
		}
	}
	var gotImport bool
	for _, ed := range res.Edges {
		if ed.Kind == graph.EdgeImports && ed.To == "unresolved::import::Foundation/Foundation.h" {
			gotImport = true
		}
	}
	assert.True(t, gotGreeter)
	assert.True(t, gotGreetSel)
	assert.True(t, gotShared)
	assert.True(t, gotHelper)
	assert.True(t, gotImport)
}

func TestObjCExtractor_EmptyInput(t *testing.T) {
	res, err := NewObjCExtractor().Extract("e.m", []byte(""))
	require.NoError(t, err)
	require.Len(t, res.Nodes, 1)
	assert.Equal(t, graph.KindFile, res.Nodes[0].Kind)
}

// TestObjcPropertyTypeAndReceiver is part of the C11 set: an @property is a
// fully-attributed field — carrying its declared type and owning class.
func TestObjcPropertyTypeAndReceiver(t *testing.T) {
	src := []byte("@interface Widget : NSObject\n" +
		"@property (nonatomic, strong) NSString *title;\n" +
		"@property NSInteger count;\n" +
		"@end\n")
	res, err := NewObjCExtractor().Extract("W.m", src)
	require.NoError(t, err)
	byName := map[string]*graph.Node{}
	for _, n := range res.Nodes {
		if n.Kind == graph.KindField {
			byName[n.Name] = n
		}
	}
	require.NotNil(t, byName["title"])
	assert.Equal(t, "NSString", byName["title"].Meta["field_type"])
	assert.Equal(t, "W.m::Widget", byName["title"].Meta["receiver"])
	require.NotNil(t, byName["count"])
	assert.Equal(t, "NSInteger", byName["count"].Meta["field_type"])
}

func TestObjCExtractor_SelectorFnValue(t *testing.T) {
	const objc = `@implementation Widget
- (void)doThing:(id)arg { }
- (void)tap {
    [self performSelector:@selector(doThing:)];
}
@end
`
	res, err := NewObjCExtractor().Extract("Widget.m", []byte(objc))
	require.NoError(t, err)

	var found *graph.Edge
	for _, e := range res.Edges {
		if e.Meta == nil {
			continue
		}
		if v, _ := e.Meta["via"].(string); v != "callback_candidate" {
			continue
		}
		if name, _ := e.Meta["fn_value_name"].(string); name == "doThing:" {
			found = e
		}
	}
	require.NotNil(t, found, "@selector(doThing:) should capture doThing: as a function value")
	assert.Equal(t, "Widget.m::tap", found.From, "captured in the enclosing method")
	assert.Equal(t, "selector", found.Meta["fn_ref_form"])
}

func TestObjCExtractor_ProtocolConformance(t *testing.T) {
	const objc = `@interface Widget : NSObject <Drawable, Tappable>
@end
`
	res, err := NewObjCExtractor().Extract("Widget.h", []byte(objc))
	require.NoError(t, err)
	var cls *graph.Node
	for _, n := range res.Nodes {
		if n.Name == "Widget" && n.Kind == graph.KindType {
			cls = n
		}
	}
	require.NotNil(t, cls, "interface Widget should be extracted")
	require.NotNil(t, cls.Meta, "Widget should carry conformance meta")
	assert.Equal(t, "Drawable,Tappable", cls.Meta["objc_protocols"])
}
