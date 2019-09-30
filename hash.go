package cache

import "unsafe"

// functions in this file taken from https://github.com/dgraph-io/ristretto

type stringStruct struct {
	str unsafe.Pointer
	len int
}

//go:noescape
//go:linkname memhash runtime.memhash
func memhash(p unsafe.Pointer, h, s uintptr) uintptr

// memHash is the hash function used by go map, it utilizes available hardware instructions(behaves
// as aeshash if aes instruction is available).
// NOTE: The hash seed changes for every process. So, this cannot be used as a persistent hash.
func memHash(data []byte) uint64 {
	ss := (*stringStruct)(unsafe.Pointer(&data))
	return uint64(memhash(ss.str, 0, uintptr(ss.len)))
}

// stringStruct is the hash function used by go map, it utilizes available hardware instructions
// (behaves as aeshash if aes instruction is available).
// NOTE: The hash seed changes for every process. So, this cannot be used as a persistent hash.
func memHashString(str string) uint64 {
	ss := (*stringStruct)(unsafe.Pointer(&str))
	return uint64(memhash(ss.str, 0, uintptr(ss.len)))
}

// KeyToHash interprets the type of key and converts it to a uint64 hash.
func keyToHash(key interface{}) uint64 {
	switch k := key.(type) {
	case uint64:
		return k
	case string:
		return memHashString(k)
	case []byte:
		return memHash(k)
	case byte:
		return memHash([]byte{k})
	case int:
		return uint64(k)
	case int32:
		return uint64(k)
	case uint32:
		return uint64(k)
	case int64:
		return uint64(k)
	default:
		panic("Key type not supported")
	}
}
