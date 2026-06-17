package languages

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zzet/gortex/internal/graph"
)

func emitEdgeTo(edges []*graph.Edge, kind graph.EdgeKind, to string) *graph.Edge {
	for _, e := range edges {
		if e.Kind == kind && e.To == to {
			return e
		}
	}
	return nil
}

func TestObjCExtractor_RNNativeEmit(t *testing.T) {
	const objc = `#import "RCTEventEmitter.h"

@implementation BatteryModule
- (void)notify {
    [self sendEventWithName:@"battery" body:@{@"level": @100}];
}
@end
`
	res, err := NewObjCExtractor().Extract("BatteryModule.m", []byte(objc))
	require.NoError(t, err)

	topicID := "event::pubsub::rn_native_event::battery"
	var topic *graph.Node
	for _, n := range res.Nodes {
		if n.ID == topicID {
			topic = n
		}
	}
	require.NotNil(t, topic, "native emit should materialise the rn_native_event topic node")
	assert.Equal(t, graph.KindEvent, topic.Kind)
	assert.Equal(t, "rn_native_event", topic.Meta["transport"])

	emit := emitEdgeTo(res.Edges, graph.EdgeEmits, topicID)
	require.NotNil(t, emit, "sendEventWithName: should emit an EdgeEmits to the topic")
	assert.Equal(t, "BatteryModule.m::notify", emit.From, "emit attributed to the enclosing method")
}

func TestSwiftExtractor_RNNativeEmit(t *testing.T) {
	const swift = `class BatteryModule {
    func notify() {
        sendEvent(withName: "battery", body: ["level": 100])
    }
}
`
	res, err := NewSwiftExtractor().Extract("BatteryModule.swift", []byte(swift))
	require.NoError(t, err)

	topicID := "event::pubsub::rn_native_event::battery"
	emit := emitEdgeTo(res.Edges, graph.EdgeEmits, topicID)
	require.NotNil(t, emit, "sendEvent(withName:) should emit an EdgeEmits to the topic")
	assert.Equal(t, "BatteryModule.swift::BatteryModule.notify", emit.From, "emit attributed to the enclosing method")
}

func TestRNNativeEventEmitterListener(t *testing.T) {
	// A JS NativeEventEmitter addListener re-homes onto the rn_native_event
	// bridge (via the react-native import) so it shares the topic node with
	// the native sendEventWithName: emitter.
	src := `import { NativeEventEmitter, NativeModules } from 'react-native';

function subscribe() {
  const emitter = new NativeEventEmitter(NativeModules.BatteryModule);
  emitter.addListener('battery', onBattery);
}
`
	fix := runTSExtractFixture(t, "app.ts", src)

	events := fix.nodesByKind[graph.KindEvent]
	require.Len(t, events, 1)
	assert.Equal(t, "event::pubsub::rn_native_event::battery", events[0].ID)
	assert.Equal(t, "rn_native_event", events[0].Meta["transport"])
	assert.Len(t, fix.edgesByKind[graph.EdgeListensOn], 1)
}
