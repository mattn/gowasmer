package main

import (
	"syscall/js"

	"github.com/mattn/gowasmer/wasmutil"
)

func Add(a, b int) int {
	return a + b
}

func main() {
	c := make(chan struct{})
	js.Global().Set("Add", js.FuncOf(wasmutil.Wrap(Add)))
	<-c
}
