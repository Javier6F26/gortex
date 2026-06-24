package languages

// Reserved call denylist for through-handler call attribution. When walking
// an inline route-handler body (`app.get('/u', (req,res) => { ... })`) to
// connect a route to the services it calls, the framework's own
// request/response/next helpers and JS built-ins are noise — `res.json()`,
// `req.params`, `next()`, `console.log()` are not application calls. This
// table (plus the handler's own parameter names, checked dynamically) is the
// filter, mirroring the table style of phpLaravelFacadesDefault.

// jsReservedCallReceivers are receiver identifiers whose member calls are
// framework/runtime helpers, not application services.
var jsReservedCallReceivers = map[string]struct{}{
	"req":      {},
	"res":      {},
	"request":  {},
	"response": {},
	"reply":    {},
	"next":     {},
	"ctx":      {},
	"context":  {},
	"console":  {},
	"JSON":     {},
	"Math":     {},
	"Object":   {},
	"Array":    {},
	"Promise":  {},
	"this":     {},
}

// jsReservedBareCalls are free-function calls that are runtime/built-in, not
// application services.
var jsReservedBareCalls = map[string]struct{}{
	"next":        {},
	"require":     {},
	"parseInt":    {},
	"parseFloat":  {},
	"String":      {},
	"Number":      {},
	"Boolean":     {},
	"setTimeout":  {},
	"setInterval": {},
	"isNaN":       {},
}

// jsIsReservedReceiver reports whether a member-call receiver name is a
// framework/runtime helper rather than an application service.
func jsIsReservedReceiver(name string) bool {
	_, ok := jsReservedCallReceivers[name]
	return ok
}

// jsIsReservedBareCall reports whether a free-function call name is a
// runtime/built-in.
func jsIsReservedBareCall(name string) bool {
	_, ok := jsReservedBareCalls[name]
	return ok
}
