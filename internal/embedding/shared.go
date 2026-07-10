package embedding

import (
	"context"
	"os"
	"strings"
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

// sharedCode memoises the process-wide code-embedding provider used by
// the rerank's semantic-cosine channel: the bundled static code model
// (potion) when its files resolve — explicit dir, exec-adjacent
// sidecar, per-user models dir, or a checksum-verified first-use
// download — and the baked GloVe word vectors otherwise, so an offline
// install without the sidecar still gets a semantic channel.
var (
	sharedCodeOnce sync.Once
	sharedCodeInst Provider
)

// SharedCodeEmbedder returns the process-wide code embedder for the
// rerank's semantic-cosine channel. Never returns an error — the
// fallback chain ends at the baked static provider; nil only when even
// that failed to load. Safe for concurrent use. GORTEX_POTION=0 pins
// the GloVe fallback (diagnostic escape hatch).
func SharedCodeEmbedder() Provider {
	sharedCodeOnce.Do(func() {
		if v := strings.TrimSpace(os.Getenv("GORTEX_POTION")); v == "0" || strings.EqualFold(v, "false") || strings.EqualFold(v, "off") {
			sharedCodeInst = staticOrNil()
			return
		}
		dir := resolvePotionDir()
		if dir == "" {
			if d, err := downloadPotion(); err == nil {
				dir = d
			}
		}
		if dir != "" {
			if p, err := NewPotionProviderFromDir(dir); err == nil {
				sharedCodeInst = p
				return
			}
		}
		sharedCodeInst = staticOrNil()
	})
	return sharedCodeInst
}

// staticOrNil adapts SharedStatic's concrete return into the Provider
// interface without wrapping a typed nil.
func staticOrNil() Provider {
	if p := SharedStatic(); p != nil {
		return p
	}
	return nil
}
