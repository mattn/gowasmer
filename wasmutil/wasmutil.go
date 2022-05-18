package wasmutil

import (
	"reflect"
	"syscall/js"
)

// Wrap make function for friendly to js/wasm.
func Wrap(f interface{}) func(js.Value, []js.Value) interface{} {
	return func(this js.Value, args []js.Value) interface{} {
		rf := reflect.ValueOf(f)
		rt := rf.Type()
		rargs := []reflect.Value{}
		for i := 0; i < rt.NumIn(); i++ {
			var arg reflect.Value
			switch args[i].Type() {
			case js.TypeUndefined:
				arg = reflect.Zero(reflect.TypeOf(nil)).Convert(rt.In(i))
			case js.TypeNull:
				arg = reflect.Zero(reflect.TypeOf(nil)).Convert(rt.In(i))
			case js.TypeBoolean:
				arg = reflect.ValueOf(args[i].Bool()).Convert(rt.In(i))
			case js.TypeNumber:
				arg = reflect.ValueOf(args[i].Float()).Convert(rt.In(i))
			case js.TypeString:
				arg = reflect.ValueOf(args[i].String()).Convert(rt.In(i))
			case js.TypeSymbol:
				arg = reflect.ValueOf(args[i].String()).Convert(rt.In(i))
			default:
				panic("type not supported")
			}
			rargs = append(rargs, arg)
		}
		ret := rf.Call(rargs)
		if len(ret) > 0 {
			return ret[0].Interface()
		}
		return nil
	}
}
