package languages

import (
	"fmt"
	"testing"
)

func TestDebugGAS(t *testing.T) {
	src := []byte(`.include "defs.s"
.globl main
.extern puts

.text
main:
    pushq %rbp
    call greet
    xorl %eax, %eax
    popq %rbp
    ret

greet:
    leaq msg(%rip), %rdi
    call puts
    ret

.data
msg:
    .asciz "hi"
`)
	e := NewAssemblyExtractor()
	res, err := e.Extract("g.s", src)
	if err != nil {
		t.Fatal(err)
	}
	for _, ed := range res.Edges {
		fmt.Printf("EDGE %s %s -> %s (line %d)\n", ed.Kind, ed.From, ed.To, ed.Line)
	}
	for _, n := range res.Nodes {
		fmt.Printf("NODE %s %s (meta=%v)\n", n.Kind, n.Name, n.Meta)
	}
}
