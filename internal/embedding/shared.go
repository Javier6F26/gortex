package embedding

import (
	"context"
	"sync"
)

// sharedStatic memoises a single process-wide StaticProvider. The baked
// GloVe vectors are ~3.7MB compressed and decompress into a ~20k-entry
// map that is safe for concurrent reads, so one instance serves every
// rerank call. Constructed lazily on first use.
var (
	sharedStaticOnce sync.Once
	sharedStaticInst *StaticProvider
)

// SharedStatic returns the process-wide static word-vector provider,
// constructing it on first call. Returns nil only when the baked
// vectors fail to load (a corrupt build); callers treat nil as "no
// semantic-cosine channel". Safe for concurrent use.
func SharedStatic() *StaticProvider {
	sharedStaticOnce.Do(func() {
		p, err := NewStaticProvider()
		if err != nil {
			return
		}
		sharedStaticInst = p
	})
	return sharedStaticInst
}

// EmbedTextFunc adapts a provider into the plain func the rerank
// Context wants: text -> normalised vector, errors and nil providers
// collapsing to a nil result the signal reads as "cannot embed".
func EmbedTextFunc(p Provider) func(string) []float32 {
	if p == nil {
		return nil
	}
	return func(text string) []float32 {
		vec, err := p.Embed(context.Background(), text)
		if err != nil {
			return nil
		}
		return vec
	}
}
