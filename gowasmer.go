package gowasmer

import (
	"encoding/binary"
	"errors"
	"fmt"
	"log"
	"math"
	"os"
	"reflect"
	"time"

	"github.com/wasmerio/wasmer-go/wasmer"
)

// GoInstance is instance of Go Runtime.
type GoInstance struct {
	inst   *wasmer.Instance
	mem    *wasmer.Memory
	getsp  wasmer.NativeFunction
	resume wasmer.NativeFunction
	values map[uint32]interface{}
	ids    map[string]uint32
}

// Get return Go value specified by name
func (d *GoInstance) Get(name string) interface{} {
	return d.values[5].(map[string]interface{})[name]
}

func (d *GoInstance) getInt32(addr int32) int32 {
	return int32(binary.LittleEndian.Uint32(d.mem.Data()[addr+0:]))
}

func (d *GoInstance) getInt64(addr int32) int64 {
	low := binary.LittleEndian.Uint32(d.mem.Data()[addr+0:])
	high := binary.LittleEndian.Uint32(d.mem.Data()[addr+4:])
	return int64(low) + int64(high)*4294967296
}

func (d *GoInstance) setInt32(addr int32, v int64) {
	binary.LittleEndian.PutUint32(d.mem.Data()[addr+0:], uint32(v))
}

func (d *GoInstance) setInt64(addr int32, v int64) {
	binary.LittleEndian.PutUint32(d.mem.Data()[addr+0:], uint32(v))
	binary.LittleEndian.PutUint32(d.mem.Data()[addr+4:], uint32(v/4294967296))
}

func (d *GoInstance) reflectSet(v interface{}, key interface{}, value interface{}) {
	if v == nil {
		v = d.values[5]
		panic(v)
	}
	if k, ok := key.(string); ok {
		v.(map[string]interface{})[k] = value
		return
	}
	i := int(key.(int64))
	arr := v.([]interface{})
	if i < 0 || i > len(arr)-1 {
		return
	}
	arr[i] = value
}

func (d *GoInstance) reflectDelete(v interface{}, key interface{}) {
	if v == nil {
		v = d.values[5]
	}
	if k, ok := key.(string); ok {
		delete(v.(map[string]interface{}), k)
		return
	}
	i := int(key.(int64))
	arr := v.([]interface{})
	if i < 0 || i > len(arr)-1 {
		copy(arr[i:], arr[i+1:])
		arr[len(arr)-1] = nil
		arr = arr[:len(arr)-1]
	}
}

func (d *GoInstance) reflectGet(v interface{}, key interface{}) interface{} {
	if v == nil {
		v = d.values[5]
	}
	if k, ok := key.(string); ok {
		return v.(map[string]interface{})[k]
	}
	i := int(key.(int64))
	arr := v.([]interface{})
	if i < 0 || i > len(arr)-1 {
		return nil
	}
	return arr[i]
}

func (d *GoInstance) loadString(addr int32) string {
	array := d.getInt64(addr + 0)
	alen := d.getInt64(addr + 8)
	return string(d.mem.Data()[array : array+alen])
}

func (d *GoInstance) loadSlice(addr int32) []byte {
	array := d.getInt64(addr + 0)
	alen := d.getInt64(addr + 8)
	return d.mem.Data()[array : array+alen]
}

func (d *GoInstance) loadValue(addr int32) interface{} {
	bits := binary.LittleEndian.Uint64(d.mem.Data()[addr+0:])
	fv := math.Float64frombits(bits)
	if fv == 0 {
		return nil
	}
	if !math.IsNaN(fv) {
		return fv
	}
	id := binary.LittleEndian.Uint32(d.mem.Data()[addr+0:])
	//fmt.Println("loadValue", id, data.values[id])
	return d.values[id]
}

func (d *GoInstance) loadSliceOfValues(addr int32) []interface{} {
	array := d.getInt64(addr + 0)
	alen := d.getInt64(addr + 8)
	results := []interface{}{}
	for i := int64(0); i < alen; i++ {
		results = append(results, d.loadValue(int32(array+i*8)))
	}
	return results
}

func (d *GoInstance) storeValue(addr int32, v interface{}) {
	nanHead := 0x7FF80000

	if vv, ok := v.(int64); ok {
		v = float64(vv)
	}
	if vv, ok := v.(int32); ok {
		v = float64(vv)
	}
	if vv, ok := v.(int); ok {
		v = float64(vv)
	}
	//fmt.Printf("storeValue %v %v\n", addr, v)
	switch t := v.(type) {
	case float64:
		if t != 0 {
			if math.IsNaN(t) {
				binary.LittleEndian.PutUint32(d.mem.Data()[addr+4:], uint32(nanHead))
				binary.LittleEndian.PutUint32(d.mem.Data()[addr+0:], 0)
				return
			}
			bits := math.Float64bits(t)
			binary.LittleEndian.PutUint64(d.mem.Data()[addr+0:], bits)
			return
		}
	default:
	}

	if v == nil {
		bits := math.Float64bits(0)
		binary.LittleEndian.PutUint64(d.mem.Data()[addr+0:], bits)
		return
	}

	s := fmt.Sprintf("%t", v)
	id, ok := d.ids[s]
	if !ok {
		id = uint32(len(d.values))
		d.ids[s] = id
	}
	d.values[id] = v

	typeFlag := 0
	switch t := v.(type) {
	case map[string]interface{}, []interface{}:
		if t != nil {
			typeFlag = 1
		}
	case string:
		typeFlag = 2
	case func([]interface{}) interface{}:
		typeFlag = 4
	}
	binary.LittleEndian.PutUint32(d.mem.Data()[addr+4:], uint32(nanHead|typeFlag))
	binary.LittleEndian.PutUint32(d.mem.Data()[addr:], id)
}

func goRuntime(store *wasmer.Store, data *GoInstance) map[string]wasmer.IntoExtern {
	data.values = map[uint32]interface{}{
		0: nil,
		1: 0,
		2: nil,
		3: true,
		4: false,
		5: map[string]interface{}{
			"console": map[string]interface{}{
				"log": func(args []interface{}) interface{} {
					fmt.Fprintln(os.Stdout, args...)
					return nil
				},
				"error": func(args []interface{}) interface{} {
					fmt.Fprintln(os.Stderr, args...)
					return nil
				},
			},
			"Object": func([]interface{}) interface{} {
				return map[string]interface{}{}
			},
			"Array": func([]interface{}) interface{} {
				return []interface{}{}
			},
		},
		6: map[string]interface{}{
			"_pendingEvent": map[string]interface{}{
				"id":   0,
				"this": nil,
			},
			"_makeFuncWrapper": func(args []interface{}) interface{} {
				id := args[0]
				return func(args []interface{}) interface{} {
					event := map[string]interface{}{
						"id":   id,
						"this": nil,
						"args": args,
					}
					data.values[6].(map[string]interface{})["_pendingEvent"] = event
					_, err := data.resume()
					if err != nil {
						log.Print("err", err)
					}
					return event["result"]
				}
			},
		},
	}
	data.ids = map[string]uint32{
		"int64": 1,
		"nil":   2,
		"true":  3,
		"false": 4,
	}
	return map[string]wasmer.IntoExtern{
		"debug": wasmer.NewFunction(
			store,
			wasmer.NewFunctionType(wasmer.NewValueTypes(wasmer.I32), wasmer.NewValueTypes()),
			func(args []wasmer.Value) ([]wasmer.Value, error) {
				sp := args[0].Unwrap()
				fmt.Println("DEBUG", sp)
				return []wasmer.Value{}, nil
			},
		),
		"runtime.resetMemoryDataView": wasmer.NewFunction(
			store,
			wasmer.NewFunctionType(wasmer.NewValueTypes(wasmer.I32), wasmer.NewValueTypes()),
			func(args []wasmer.Value) ([]wasmer.Value, error) {
				//println("runtime.resetMemoryDataView")
				return []wasmer.Value{}, nil
			},
		),
		"runtime.wasmExit": wasmer.NewFunction(
			store,
			wasmer.NewFunctionType(wasmer.NewValueTypes(wasmer.I32), wasmer.NewValueTypes()),
			func(args []wasmer.Value) ([]wasmer.Value, error) {
				//println("runtime.wasmExit")
				sp := args[0].I32()
				sp >>= 0
				os.Exit(int(data.getInt32(sp + 8)))
				return []wasmer.Value{}, nil
			},
		),
		"runtime.wasmWrite": wasmer.NewFunction(
			store,
			wasmer.NewFunctionType(wasmer.NewValueTypes(wasmer.I32), wasmer.NewValueTypes()),
			func(args []wasmer.Value) ([]wasmer.Value, error) {
				sp := args[0].I32()
				sp >>= 0
				fd := data.getInt64(sp + 8)
				p := data.getInt64(sp + 16)
				n := data.getInt32(sp + 24)
				switch fd {
				case 1:
					os.Stdout.Write(data.mem.Data()[p : p+int64(n)])
				case 2:
					os.Stderr.Write(data.mem.Data()[p : p+int64(n)])
				}
				return []wasmer.Value{}, nil
			},
		),
		"runtime.nanotime1": wasmer.NewFunction(
			store,
			wasmer.NewFunctionType(wasmer.NewValueTypes(wasmer.I32), wasmer.NewValueTypes()),
			func(args []wasmer.Value) ([]wasmer.Value, error) {
				//println("runtime.nanotime1")
				sp := args[0].I32()
				sp >>= 0
				data.setInt64(sp+8, time.Now().UnixNano())
				return []wasmer.Value{}, nil
			},
		),
		"runtime.walltime": wasmer.NewFunction(
			store,
			wasmer.NewFunctionType(wasmer.NewValueTypes(wasmer.I32), wasmer.NewValueTypes()),
			func(args []wasmer.Value) ([]wasmer.Value, error) {
				//println("runtime.walltime")
				sp := args[0].I32()
				sp >>= 0
				msec := time.Now().UnixNano() / int64(time.Millisecond)
				data.setInt64(sp+8, msec/1000)
				data.setInt32(sp+16, (msec%1000)*1000000)
				return []wasmer.Value{}, nil
			},
		),
		"runtime.scheduleTimeoutEvent": wasmer.NewFunction(
			store,
			wasmer.NewFunctionType(wasmer.NewValueTypes(wasmer.I32), wasmer.NewValueTypes()),
			func(args []wasmer.Value) ([]wasmer.Value, error) {
				//println("runtime.scheduleTimeoutEvent")
				return []wasmer.Value{}, nil
			},
		),
		"runtime.clearTimeoutEvent": wasmer.NewFunction(
			store,
			wasmer.NewFunctionType(wasmer.NewValueTypes(wasmer.I32), wasmer.NewValueTypes()),
			func(args []wasmer.Value) ([]wasmer.Value, error) {
				//println("runtime.clearTimeoutEvent")
				return []wasmer.Value{}, nil
			},
		),
		"runtime.getRandomData": wasmer.NewFunction(
			store,
			wasmer.NewFunctionType(wasmer.NewValueTypes(wasmer.I32), wasmer.NewValueTypes()),
			func(args []wasmer.Value) ([]wasmer.Value, error) {
				//println("runtime.getRandomData")
				sp := args[0].I32()
				sp >>= 0
				data.loadSlice(sp + 8)
				return []wasmer.Value{}, nil
			},
		),
		"syscall/js.finalizeRef": wasmer.NewFunction(
			store,
			wasmer.NewFunctionType(wasmer.NewValueTypes(wasmer.I32), wasmer.NewValueTypes()),
			func(args []wasmer.Value) ([]wasmer.Value, error) {
				//println("syscall/js.finalizeRef")
				return []wasmer.Value{}, nil
			},
		),
		"syscall/js.stringVal": wasmer.NewFunction(
			store,
			wasmer.NewFunctionType(wasmer.NewValueTypes(wasmer.I32), wasmer.NewValueTypes()),
			func(args []wasmer.Value) ([]wasmer.Value, error) {
				//println("syscall/js.stringVal")
				sp := args[0].I32()
				sp >>= 0
				data.storeValue(sp+24, data.loadString(sp+8))
				return []wasmer.Value{}, nil
			},
		),
		"syscall/js.valueGet": wasmer.NewFunction(
			store,
			wasmer.NewFunctionType(wasmer.NewValueTypes(wasmer.I32), wasmer.NewValueTypes()),
			func(args []wasmer.Value) ([]wasmer.Value, error) {
				//println("syscall/js.valueGet")
				sp := args[0].I32()
				sp >>= 0
				result := data.reflectGet(data.loadValue(sp+8), data.loadString(sp+16))
				if v, err := data.getsp(); err == nil {
					sp = v.(int32)
					sp >>= 0
					data.storeValue(sp+32, result)
				}
				return []wasmer.Value{}, nil
			},
		),
		"syscall/js.valueSet": wasmer.NewFunction(
			store,
			wasmer.NewFunctionType(wasmer.NewValueTypes(wasmer.I32), wasmer.NewValueTypes()),
			func(args []wasmer.Value) ([]wasmer.Value, error) {
				//println("syscall/js.valueSet")
				sp := args[0].I32()
				sp >>= 0
				data.reflectSet(data.loadValue(sp+8), data.loadString(sp+16), data.loadValue(sp+32))
				return []wasmer.Value{}, nil
			},
		),
		"syscall/js.valueDelete": wasmer.NewFunction(
			store,
			wasmer.NewFunctionType(wasmer.NewValueTypes(wasmer.I32), wasmer.NewValueTypes()),
			func(args []wasmer.Value) ([]wasmer.Value, error) {
				//println("syscall/js.valueDelete")
				sp := args[0].I32()
				sp >>= 0
				data.reflectDelete(data.loadValue(sp+8), data.loadString(sp+16))
				return []wasmer.Value{}, nil
			},
		),
		"syscall/js.valueIndex": wasmer.NewFunction(
			store,
			wasmer.NewFunctionType(wasmer.NewValueTypes(wasmer.I32), wasmer.NewValueTypes()),
			func(args []wasmer.Value) ([]wasmer.Value, error) {
				//println("syscall/js.valueIndex")
				sp := args[0].I32()
				sp >>= 0
				data.storeValue(sp+24, data.reflectGet(data.loadValue(sp+8), data.getInt64(sp+16)))
				return []wasmer.Value{}, nil
			},
		),
		"syscall/js.valueSetIndex": wasmer.NewFunction(
			store,
			wasmer.NewFunctionType(wasmer.NewValueTypes(wasmer.I32), wasmer.NewValueTypes()),
			func(args []wasmer.Value) ([]wasmer.Value, error) {
				//println("syscall/js.valueSetIndex")
				sp := args[0].I32()
				sp >>= 0
				data.reflectSet(data.loadValue(sp+8), data.getInt64(sp+16), data.loadValue(sp+24))
				return []wasmer.Value{}, nil
			},
		),
		"syscall/js.valueInvoke": wasmer.NewFunction(
			store,
			wasmer.NewFunctionType(wasmer.NewValueTypes(wasmer.I32), wasmer.NewValueTypes()),
			func(args []wasmer.Value) ([]wasmer.Value, error) {
				//println("syscall/js.valueInvoke")
				sp := args[0].I32()
				sp >>= 0
				method := reflect.ValueOf(data.loadValue(sp + 8))
				result := method.Call([]reflect.Value{reflect.ValueOf(data.loadSliceOfValues(sp + 16))})
				if v, err := data.getsp(); err == nil {
					sp = v.(int32)
					sp >>= 0
					if len(result) > 0 {
						data.storeValue(sp+40, result[0].Interface())
					}
					data.mem.Data()[sp+48] = 1
				}
				return []wasmer.Value{}, nil
			},
		),
		"syscall/js.valueCall": wasmer.NewFunction(
			store,
			wasmer.NewFunctionType(wasmer.NewValueTypes(wasmer.I32), wasmer.NewValueTypes()),
			func(args []wasmer.Value) ([]wasmer.Value, error) {
				//println("syscall/js.valueCall")
				sp := args[0].I32()
				sp >>= 0
				v := reflect.ValueOf(data.loadValue(sp + 8))
				if !v.IsValid() {
					return nil, errors.New("cannot call on nil value")
				}
				method := v.MapIndex(reflect.ValueOf(data.loadString(sp + 16)))
				if !v.IsValid() {
					return nil, errors.New("cannot find method on the value")
				}
				result := method.Elem().Call([]reflect.Value{reflect.ValueOf(data.loadSliceOfValues(sp + 32))})
				if v, err := data.getsp(); err == nil {
					sp = v.(int32)
					sp >>= 0
					if len(result) > 0 {
						data.storeValue(sp+56, result[0].Interface())
					}
					data.mem.Data()[sp+64] = 1
				}
				return []wasmer.Value{}, nil
			},
		),
		"syscall/js.valueNew": wasmer.NewFunction(
			store,
			wasmer.NewFunctionType(wasmer.NewValueTypes(wasmer.I32), wasmer.NewValueTypes()),
			func(args []wasmer.Value) ([]wasmer.Value, error) {
				//println("syscall/js.valueNew")
				return []wasmer.Value{}, nil
			},
		),
		"syscall/js.valueLength": wasmer.NewFunction(
			store,
			wasmer.NewFunctionType(wasmer.NewValueTypes(wasmer.I32), wasmer.NewValueTypes()),
			func(args []wasmer.Value) ([]wasmer.Value, error) {
				//println("syscall/js.valueLength")
				sp := args[0].I32()
				sp >>= 0
				v, ok := data.loadValue(sp + 8).([]interface{})
				if ok {
					data.setInt64(sp+16, int64(len(v)))
				}
				return []wasmer.Value{}, nil
			},
		),
		"syscall/js.valuePrepareString": wasmer.NewFunction(
			store,
			wasmer.NewFunctionType(wasmer.NewValueTypes(wasmer.I32), wasmer.NewValueTypes()),
			func(args []wasmer.Value) ([]wasmer.Value, error) {
				//println("syscall/js.valuePrepareString")
				return []wasmer.Value{}, nil
			},
		),
		"syscall/js.valueLoadString": wasmer.NewFunction(
			store,
			wasmer.NewFunctionType(wasmer.NewValueTypes(wasmer.I32), wasmer.NewValueTypes()),
			func(args []wasmer.Value) ([]wasmer.Value, error) {
				//println("syscall/js.valueLoadString")
				return []wasmer.Value{}, nil
			},
		),
		"syscall/js.valueInstanceOf": wasmer.NewFunction(
			store,
			wasmer.NewFunctionType(wasmer.NewValueTypes(wasmer.I32), wasmer.NewValueTypes()),
			func(args []wasmer.Value) ([]wasmer.Value, error) {
				//println("syscall/js.valueInstanceOf")
				return []wasmer.Value{}, nil
			},
		),
		"syscall/js.copyBytesToGo": wasmer.NewFunction(
			store,
			wasmer.NewFunctionType(wasmer.NewValueTypes(wasmer.I32), wasmer.NewValueTypes()),
			func(args []wasmer.Value) ([]wasmer.Value, error) {
				//println("syscall/js.copyBytesToJS")
				sp := args[0].I32()
				sp >>= 0
				dst := data.loadSlice(sp + 8)
				src := data.loadSlice(sp + 32)
				if len(dst) == 0 || len(src) == 0 {
					data.mem.Data()[sp+48] = 0
				} else {
					toCopy := src[0:len(dst)]
					copy(dst, toCopy)
					data.setInt64(sp+40, int64(len(toCopy)))
					data.mem.Data()[sp+48] = 1
				}
				return []wasmer.Value{}, nil
			},
		),
		"syscall/js.copyBytesToJS": wasmer.NewFunction(
			store,
			wasmer.NewFunctionType(wasmer.NewValueTypes(wasmer.I32), wasmer.NewValueTypes()),
			func(args []wasmer.Value) ([]wasmer.Value, error) {
				//println("syscall/js.copyBytesToJS")
				sp := args[0].I32()
				sp >>= 0
				dst := data.loadSlice(sp + 8)
				src := data.loadSlice(sp + 32)
				if len(dst) == 0 || len(src) == 0 {
					data.mem.Data()[sp+48] = 0
				} else {
					toCopy := src[0:len(dst)]
					copy(dst, toCopy)
					data.setInt64(sp+40, int64(len(toCopy)))
					data.mem.Data()[sp+48] = 1
				}
				return []wasmer.Value{}, nil
			},
		),
	}
}

// NewInstance create instance of GoRuntime.
func NewInstance(b []byte) (*GoInstance, error) {
	engine := wasmer.NewEngine()
	store := wasmer.NewStore(engine)
	module, err := wasmer.NewModule(store, b)
	if err != nil {
		return nil, err
	}

	data := &GoInstance{}
	importObject := wasmer.NewImportObject()
	importObject.Register("go", goRuntime(store, data))

	instance, err := wasmer.NewInstance(module, importObject)
	if err != nil {
		return nil, err
	}
	data.inst = instance

	mem, err := instance.Exports.GetMemory("mem")
	if err != nil {
		return nil, err
	}
	data.mem = mem

	offset := 4096
	strPtr := func(str string) int {
		ptr := offset
		b := append([]byte(str), 0)
		copy(data.mem.Data()[offset:offset+len(b)], b)
		offset += len(b)
		if offset%8 != 0 {
			offset += 8 - (offset % 8)
		}
		return ptr
	}
	argPtrs := []int{strPtr("js"), 0, 0}
	for _, ptr := range argPtrs {
		binary.LittleEndian.PutUint32(data.mem.Data()[offset+0:], uint32(ptr))
		binary.LittleEndian.PutUint32(data.mem.Data()[offset+4:], 0)
		offset += 8
	}

	getsp, err := instance.Exports.GetFunction("getsp")
	if err != nil {
		return nil, err
	}
	data.getsp = getsp

	resume, err := instance.Exports.GetFunction("resume")
	if err != nil {
		return nil, err
	}
	data.resume = resume

	run, err := instance.Exports.GetFunction("run")
	if err != nil {
		return nil, err
	}
	_, err = run(1, 4104)
	if err != nil {
		return nil, err
	}

	return data, nil
}
