package progress

import "github.com/charmbracelet/lipgloss"

// Exported palette — kept as a thin re-export layer over the package-private
// `col*` constants so the rest of the binary (cmd/gortex, internal/tui) can
// share one source of truth for brand colour without each callsite hard-coding
// hex literals.
//
// Anything inside internal/progress can keep using the short private names;
// callers outside the package should reach for these exported variants.
var (
	ColorAccent   = colAccent
	ColorPerim    = colPerim
	ColorInner    = colInner
	ColorFg       = colFg
	ColorFgDim    = colFgDim
	ColorErr      = colErr
	ColorWarn     = colWarn
	ColorMuted    = colMuted
	ColorBorder   = colBorder
	ColorInfoSoft = colInfoSoft
)

// Exported styles. Lipgloss styles are immutable on chaining (every setter
// returns a new copy) so exposing the package-level globals is safe — callers
// cannot mutate them in place.
var (
	StyleAccent  = styleAccent
	StyleLabel   = styleLabel
	StyleSub     = styleSub
	StyleOK      = styleOK
	StyleErr     = styleX
	StyleHeading = styleHeading
	StyleCount   = styleCount
	StyleKey     = styleKey
	StyleVal     = styleVal
	StyleHint    = styleHint
	StyleStep    = styleStep
	StyleStrong  = styleStrong
	StyleBox     = styleBox
)

// MeshLogo renders one static frame of the gortex mesh logo with the active
// node fixed at tick % 12. Used by banner panels that want the brand mark
// without owning a live spinner.
func MeshLogo(tick int) string {
	return meshFrame(tick)
}

// MeshLogoLines returns the number of vertical rows the mesh logo occupies.
// Exported so wizard / dashboard layouts can reserve space without re-counting
// the constant.
func MeshLogoLines() int { return 5 }

// MeshLogoWidth returns the rendered visual width of the mesh logo in cells.
// 5 cells × "● " spacing = 9 chars.
func MeshLogoWidth() int { return 9 }

// PaletteFg / PaletteAccent / PaletteErr expose the resolved lipgloss colors
// for callers that need to apply them to a freshly-built style (rather than
// re-using one of the pre-composed styles above). Returned values are
// lipgloss.Color, ready to feed into any lipgloss.NewStyle().Foreground call.
func PaletteFg() lipgloss.Color     { return colFg }
func PaletteAccent() lipgloss.Color { return colAccent }
func PaletteErr() lipgloss.Color    { return colErr }
