package utils

import (
	"reflect"
	"strconv"
)

// IsNil 识别 interface 中的带类型 nil，防止调用空指针流或 Transport。
func IsNil(value any) bool {
	if value == nil {
		return true
	}
	v := reflect.ValueOf(value)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return v.IsNil()
	default:
		return false
	}
}

// ToStrings 接受标量及一层切片/数组，包含用户定义的基础类型。
// 不使用任意 Stringer，避免隐藏副作用；不支持的值由表单构造器明确报错。
func ToStrings(value any, excludeSlice bool) ([]string, bool) {
	if value == nil {
		return nil, false
	}
	v := reflect.ValueOf(value)
	if !excludeSlice && (v.Kind() == reflect.Slice || v.Kind() == reflect.Array) {
		result := make([]string, 0, v.Len())
		for i := 0; i < v.Len(); i++ {
			values, ok := ToStrings(v.Index(i).Interface(), true)
			if !ok {
				return nil, false
			}
			result = append(result, values...)
		}
		return result, true
	}
	var str string
	switch v.Kind() {
	case reflect.String:
		str = v.String()
	case reflect.Bool:
		str = strconv.FormatBool(v.Bool())
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		str = strconv.FormatInt(v.Int(), 10)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		str = strconv.FormatUint(v.Uint(), 10)
	case reflect.Float32, reflect.Float64:
		str = strconv.FormatFloat(v.Float(), 'g', -1, v.Type().Bits())
	default:
		return nil, false
	}
	return []string{str}, true
}
