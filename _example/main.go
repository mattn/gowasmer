package main

import (
	"fmt"
	"io/ioutil"
	"log"

	gowasmer "github.com/mattn/gowasmer"
)

func main() {
	b, err := ioutil.ReadFile("wasm/wasm-example")
	if err != nil {
		log.Fatal(err)
	}

	inst, err := gowasmer.NewInstance(b)
	if err != nil {
		log.Fatal(err)
	}

	m := inst.Get("Add")
	r := m.(func([]interface{}) interface{})([]interface{}{1, 3})
	fmt.Println(r)
}
