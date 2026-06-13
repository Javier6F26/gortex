package cfg

import (
	"fmt"
	"strings"
)

// mermaidMaxStmts caps the statement lines rendered per block so a
// long straight-line block doesn't dominate the diagram.
const mermaidMaxStmts = 8

// Mermaid renders the CFG as a Mermaid flowchart. Entry/exit get
// stadium shapes, every other block lists its statements. Sequential
// edges are unlabeled; every other label rides on the arrow.
func (c *CFG) Mermaid() string {
	var b strings.Builder
	b.WriteString("flowchart TD\n")
	for _, bl := range c.Blocks {
		if len(bl.Stmts) == 0 && !c.hasEdgeAt(bl.ID) {
			continue // orphan empty block — noise in the diagram
		}
		switch bl.ID {
		case c.Entry:
			fmt.Fprintf(&b, "    B%d([\"entry%s\"])\n", bl.ID, mermaidStmts(bl, true))
		case c.Exit:
			fmt.Fprintf(&b, "    B%d([\"exit\"])\n", bl.ID)
		default:
			body := mermaidStmts(bl, false)
			if body == "" {
				body = bl.Label
			}
			fmt.Fprintf(&b, "    B%d[\"%s\"]\n", bl.ID, body)
		}
	}
	for _, e := range c.Edges {
		if e.Label == LabelSeq {
			fmt.Fprintf(&b, "    B%d --> B%d\n", e.From, e.To)
		} else {
			fmt.Fprintf(&b, "    B%d -->|%s| B%d\n", e.From, string(e.Label), e.To)
		}
	}
	return b.String()
}

// hasEdgeAt reports whether any edge touches the block.
func (c *CFG) hasEdgeAt(id int) bool {
	for _, e := range c.Edges {
		if e.From == id || e.To == id {
			return true
		}
	}
	return false
}

// mermaidStmts renders a block's statements as <br/>-joined lines.
func mermaidStmts(bl *Block, contLine bool) string {
	if len(bl.Stmts) == 0 {
		return ""
	}
	parts := make([]string, 0, len(bl.Stmts)+1)
	if contLine {
		parts = append(parts, "")
	}
	for i, st := range bl.Stmts {
		if i == mermaidMaxStmts {
			parts = append(parts, fmt.Sprintf("… +%d more", len(bl.Stmts)-i))
			break
		}
		parts = append(parts, fmt.Sprintf("L%d: %s", st.StartLine, mermaidEscape(st.Text)))
	}
	return strings.Join(parts, "<br/>")
}

// mermaidEscape neutralizes the characters Mermaid treats as node
// syntax inside a quoted label.
func mermaidEscape(s string) string {
	r := strings.NewReplacer(
		"\"", "#quot;",
		"<", "#lt;",
		">", "#gt;",
		"{", "#123;",
		"}", "#125;",
		"[", "#91;",
		"]", "#93;",
		"|", "#124;",
	)
	return r.Replace(s)
}
