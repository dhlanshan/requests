package tools

func TernaryOperator[T any](exp bool, e1, e2 T) T {
	if exp {
		return e1
	}
	return e2
}

func InSlice[T comparable](items []T, value T) bool {
	for _, item := range items {
		if item == value {
			return true
		}
	}

	return false
}

func GetMapDefault[K comparable, V any](m map[K]V, key K, defaultValue V) V {
	if value, exists := m[key]; exists {
		return value
	}
	return defaultValue
}
