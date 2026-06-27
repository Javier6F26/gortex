package languages

import (
	"regexp"

	"github.com/zzet/gortex/internal/parser"
)

// rnNativeEventTransport is the pub/sub transport label for a React Native
// native-module event channel: an Objective-C / Swift `sendEventWithName:`
// emit paired with a JS `NativeEventEmitter(...).addListener` handler. The
// `rn_` prefix marks it as a native cross-language bridge the event-channel
// synthesizer pairs (and the contracts broker layer ignores).
const rnNativeEventTransport = "rn_native_event"

// rnObjCSendEventRe matches an Objective-C `[self sendEventWithName:@"Name" …]`
// RCTEventEmitter emit, capturing the event name string.
var rnObjCSendEventRe = regexp.MustCompile(`sendEventWithName:\s*@"([^"]+)"`)

// rnSendEventWrapperRe matches a paren-form `sendEvent(...)` emit -- both the
// Swift labelled `sendEvent(withName: "Name", ...)` and a custom helper
// wrapper `sendEvent(reactContext, "Name", body)` -- capturing the first
// literal string argument as the event name. The `[^;{}]` guard keeps a
// single match from spanning a statement boundary.
var rnSendEventWrapperRe = regexp.MustCompile(`\bsendEvent\s*\([^;{}]*?"([^"]+)"`)

// mineRNNativeEmits scans native source for React Native event-emit sites and
// records one pub/sub publish per emit on the rn_native_event channel,
// attributed to the enclosing function via callerLookup. The event-channel
// synthesizer then pairs each native emit with the JS addListener handler on
// the same event name. File-scope emits (callerLookup returns "") are dropped.
func mineRNNativeEmits(src []byte, re *regexp.Regexp, callerLookup func(line int) string, filePath, language string, result *parser.ExtractionResult) {
	matches := re.FindAllSubmatchIndex(src, -1)
	if len(matches) == 0 {
		return
	}
	events := make([]pubsubEvent, 0, len(matches))
	for _, m := range matches {
		name := string(src[m[2]:m[3]])
		if name == "" {
			continue
		}
		events = append(events, pubsubEvent{
			role:      pubsubRolePublish,
			transport: rnNativeEventTransport,
			topic:     name,
			method:    "sendEventWithName",
			line:      lineAt(src, m[0]),
		})
	}
	emitPubsubEvents(events, callerLookup, filePath, language, result)
}
