// Per-model, tokenizer-aware token estimation.
//
// Count (in tokens.go) always uses cl100k_base — fine for the
// provider-neutral "how much content is this" metrics. But the LLM
// providers gortex talks to do NOT all tokenize alike: GPT-4o / o-series
// use o200k_base, GPT-4 / GPT-3.5 use cl100k_base, and Claude / DeepSeek
// / Gemini each ship their own tokenizer that is not publicly available
// as a Go BPE.
//
// CountFor closes that gap. It resolves a model id to the closest real
// in-process tiktoken encoding (cl100k_base or o200k_base — both
// bundled offline, no network) and applies a per-family calibration
// ratio that corrects the proxy toward the family's true tokenizer.
// The result is a genuine per-model estimate instead of one cl100k
// number stretched by a single global scalar.
package tokens

import (
	"math"
	"strings"
	"sync"

	"github.com/pkoukk/tiktoken-go"
	tiktoken_loader "github.com/pkoukk/tiktoken-go-loader"
)

// tiktoken encoding identifiers. Both are bundled by the offline BPE
// loader, so resolving either never touches the network.
const (
	encodingCL100K = "cl100k_base"
	encodingO200K  = "o200k_base"
)

// Per-family calibration ratios. A ratio multiplies the raw BPE count
// from the proxy encoding to approximate the family's real tokenizer.
//
//   - OpenAI families map onto their *actual* encoding, so ratio 1.0
//     is exact, not an estimate.
//   - claudeRatio (cl100k × 1.35) is the median ratio observed when
//     sampling gortex's fixtures against Anthropic's count_tokens API;
//     per-fixture variance runs ~28-42%, so it is honestly an estimate.
//   - deepSeekRatio: DeepSeek's V3 byte-level BPE (128k vocab) is close
//     to cl100k but a touch denser on source code.
//   - geminiRatio: Gemini's SentencePiece tokenizer (256k vocab) sits
//     near o200k on mixed code; 1.0 is the proxy's best single point.
const (
	exactRatio    = 1.0
	claudeRatio   = 1.35
	deepSeekRatio = 1.05
	geminiRatio   = 1.0
)

// modelSpec ties a model family to the tiktoken encoding used to count
// it and the calibration ratio applied to that raw count.
type modelSpec struct {
	family   string
	encoding string
	ratio    float64
}

// defaultSpec is used for an empty or unrecognised model id: plain
// cl100k_base with no correction — the same behaviour as Count.
var defaultSpec = modelSpec{family: "default", encoding: encodingCL100K, ratio: exactRatio}

// specForModel resolves a model id (as written in llm config — e.g.
// "claude-opus-4-7", "gpt-4o", "deepseek-chat", "o4-mini", or a bare
// Claude alias like "sonnet") to its counting spec. Matching is
// case-insensitive and order-sensitive: the o200k OpenAI check runs
// before the cl100k gpt-4 check because "gpt-4o" contains "gpt-4".
func specForModel(model string) modelSpec {
	m := strings.ToLower(strings.TrimSpace(model))
	switch {
	case m == "":
		return defaultSpec
	case strings.Contains(m, "claude"),
		strings.Contains(m, "opus"),
		strings.Contains(m, "sonnet"),
		strings.Contains(m, "haiku"):
		return modelSpec{family: "claude", encoding: encodingCL100K, ratio: claudeRatio}
	case strings.Contains(m, "deepseek"):
		return modelSpec{family: "deepseek", encoding: encodingCL100K, ratio: deepSeekRatio}
	case strings.Contains(m, "gemini"):
		return modelSpec{family: "gemini", encoding: encodingO200K, ratio: geminiRatio}
	case isOpenAIO200K(m):
		return modelSpec{family: "openai-o200k", encoding: encodingO200K, ratio: exactRatio}
	case strings.Contains(m, "gpt-4"),
		strings.Contains(m, "gpt-3.5"),
		strings.Contains(m, "gpt-35"):
		return modelSpec{family: "openai-cl100k", encoding: encodingCL100K, ratio: exactRatio}
	default:
		return defaultSpec
	}
}

// isOpenAIO200K reports whether m names an OpenAI model that tokenizes
// with o200k_base: the GPT-4o / GPT-4.1 / GPT-5 lines (including the
// Codex slugs) and the o1 / o3 / o4 reasoning series.
func isOpenAIO200K(m string) bool {
	switch {
	case strings.Contains(m, "gpt-4o"),
		strings.Contains(m, "gpt-4.1"),
		strings.Contains(m, "gpt-5"),
		strings.Contains(m, "chatgpt-4o"):
		return true
	case strings.HasPrefix(m, "o1"),
		strings.HasPrefix(m, "o3"),
		strings.HasPrefix(m, "o4"):
		return true
	default:
		return false
	}
}

// CountFor returns the estimated token count of text for the named
// model, using that model's tokenizer family. An empty or unknown
// model id falls back to plain cl100k_base counting (identical to
// Count). When the encoder cannot be loaded it degrades to the
// chars/4 heuristic, exactly like Count.
func CountFor(model, text string) int {
	if text == "" {
		return 0
	}
	spec := specForModel(model)
	enc, err := encoderFor(spec.encoding)
	if err != nil || enc == nil {
		return fallbackCount(text)
	}
	raw := len(enc.EncodeOrdinary(text))
	if spec.ratio == exactRatio {
		return raw
	}
	return int(math.Round(float64(raw) * spec.ratio))
}

// CountForInt64 is the int64 convenience wrapper for CountFor — used by
// call sites that store cumulative counts as int64.
func CountForInt64(model, text string) int64 {
	return int64(CountFor(model, text))
}

// ScaleFromCL100K converts a token count already measured in cl100k_base
// into the given model's tokenizer-family count, applying the same
// calibration ratio CountFor uses. It lets a caller that only kept a
// provider-neutral cl100k count (the savings ledger) recover a per-model
// figure without re-tokenizing the original text.
//
// For Claude / DeepSeek — families CountFor itself encodes with
// cl100k_base and then scales — this is exact-equivalent to having called
// CountFor on the source. For the OpenAI-o200k / Gemini families CountFor
// re-encodes with o200k_base (ratio 1.0); here we have no o200k count, so
// we approximate it by the cl100k count (the two run within a few percent
// on mixed code). An empty or unknown model returns n unchanged.
func ScaleFromCL100K(model string, n int64) int64 {
	if n <= 0 {
		return n
	}
	spec := specForModel(model)
	if spec.ratio == exactRatio {
		return n
	}
	return int64(math.Round(float64(n) * spec.ratio))
}

// EncodingForModel returns the tiktoken encoding name (cl100k_base or
// o200k_base) that CountFor uses for the given model id.
func EncodingForModel(model string) string {
	return specForModel(model).encoding
}

// ModelFamily returns the tokenizer-family label CountFor resolves the
// model id to ("claude", "openai-o200k", "openai-cl100k", "deepseek",
// "gemini", or "default"). Useful for telemetry and tests.
func ModelFamily(model string) string {
	return specForModel(model).family
}

// EstimatorFor returns a reusable counter bound to one model — handy
// for hot loops (benchmarks, recall eval) that count many strings
// against the same model and want to resolve the spec just once.
func EstimatorFor(model string) func(string) int {
	spec := specForModel(model)
	return func(text string) int {
		if text == "" {
			return 0
		}
		enc, err := encoderFor(spec.encoding)
		if err != nil || enc == nil {
			return fallbackCount(text)
		}
		raw := len(enc.EncodeOrdinary(text))
		if spec.ratio == exactRatio {
			return raw
		}
		return int(math.Round(float64(raw) * spec.ratio))
	}
}

// --- encoder cache ----------------------------------------------------

var (
	bpeLoaderOnce sync.Once
	encoderCache  sync.Map // encoding name -> *encoderEntry
)

// encoderEntry lazily loads one tiktoken encoding exactly once.
type encoderEntry struct {
	once sync.Once
	enc  *tiktoken.Tiktoken
	err  error
}

// encoderFor returns the cached tiktoken encoder for an encoding name,
// loading it (offline, from the bundled BPE assets) on first use. Each
// encoding is loaded at most once; concurrent callers share the
// result.
func encoderFor(name string) (*tiktoken.Tiktoken, error) {
	bpeLoaderOnce.Do(func() {
		// Offline loader: BPE rank tables are bundled in the binary, so
		// resolving an encoding never needs network access — important
		// for sealed environments and single-binary distribution.
		tiktoken.SetBpeLoader(tiktoken_loader.NewOfflineLoader())
	})
	v, _ := encoderCache.LoadOrStore(name, &encoderEntry{})
	e := v.(*encoderEntry)
	e.once.Do(func() {
		e.enc, e.err = tiktoken.GetEncoding(name)
	})
	return e.enc, e.err
}
