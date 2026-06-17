package languages

import (
	"testing"

	"github.com/zzet/gortex/internal/graph"
)

func TestObjCExtractor_Depth(t *testing.T) {
	const objc = `@interface Calculator : NSObject
@property (nonatomic, assign) NSInteger total;
- (void)addValue:(NSInteger)v;
@end

@implementation Calculator
- (void)addValue:(NSInteger)v {
    self.total = [self compute:v];
    [self.delegate didUpdate:self.total];
}
- (NSInteger)compute:(NSInteger)v {
    return v * 2;
}
@end

typedef NS_ENUM(NSInteger, Mode) {
    ModeOn,
    ModeOff
};
`
	res, err := NewObjCExtractor().Extract("Calculator.m", []byte(objc))
	if err != nil {
		t.Fatal(err)
	}

	nodesByName := map[string]*graph.Node{}
	for _, n := range res.Nodes {
		nodesByName[n.Name] = n
	}
	refs := map[string]bool{}      // selector references from any method
	var sawProperty, sawPropMember bool
	for _, e := range res.Edges {
		if e.Kind == graph.EdgeReferences && e.From == "Calculator.m::addValue:" {
			const pfx = "unresolved::"
			if len(e.To) > len(pfx) && e.To[:len(pfx)] == pfx {
				refs[e.To[len(pfx):]] = true
			}
		}
		if e.Kind == graph.EdgeMemberOf && e.To == "Calculator.m::Calculator" {
			sawPropMember = true
		}
	}

	// Message-send call edges from addValue: to the messaged selectors.
	if !refs["compute:"] {
		t.Errorf("missing message-send call edge addValue: -> compute: (refs %v)", refs)
	}
	if !refs["didUpdate:"] {
		t.Errorf("missing message-send call edge addValue: -> didUpdate: (refs %v)", refs)
	}

	// @property becomes a field of its class.
	if p := nodesByName["total"]; p == nil || p.Kind != graph.KindField {
		t.Errorf("@property 'total' should be a field node, got %+v", p)
	} else {
		sawProperty = true
	}
	if !sawProperty {
		t.Error("@property 'total' not extracted")
	}
	if !sawPropMember {
		t.Error("@property 'total' should be member_of Calculator")
	}

	// typedef NS_ENUM becomes a type.
	if m := nodesByName["Mode"]; m == nil || m.Kind != graph.KindType {
		t.Errorf("typedef NS_ENUM 'Mode' should be a type node, got %+v", m)
	}
}
