package utils

import (
	"fmt"
	"reflect"
	"strconv"
)

func ToStrings(v any, excludeSlice bool) ([]string, bool) {
	if v == nil {
		return nil, false
	}

	if !excludeSlice {
		rv := reflect.ValueOf(v)
		if rv.Kind() == reflect.Slice || rv.Kind() == reflect.Array {
			var result []string
			for i := 0; i < rv.Len(); i++ {
				s, ok := ToStrings(rv.Index(i).Interface(), true)
				if !ok {
					return nil, false
				}
				result = append(result, s...)
			}
			return result, true
		}
	}

	switch nv := v.(type) {
	case string:
		return []string{nv}, true
	case int:
		return []string{strconv.Itoa(nv)}, true
	case int8, int16, int32, int64:
		return []string{fmt.Sprintf("%d", nv)}, true
	case uint, uint8, uint16, uint32, uint64:
		return []string{fmt.Sprintf("%d", nv)}, true
	case float32, float64:
		return []string{fmt.Sprintf("%v", nv)}, true
	case bool:
		return []string{strconv.FormatBool(nv)}, true
	default:
		return nil, false
	}
}
