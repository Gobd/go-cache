# go-cache

go-cache is an in-memory key:value store/cache similar to memcached that is
suitable for applications running on a single machine. Its major advantage is
that, being essentially a thread-safe `map[interface{}]interface{}` with expiration
times, it doesn't need to serialize or transmit its contents over the network.

Any object can be stored, for a given duration or forever, and the cache can be
safely used by multiple goroutines.

# Changes from the original

Removed Increment, Decrement, Add, GetWithExpiration, Expired, Replace, Save, Load, Newfrom.

Keys are now interfaces rather than strings. These will be internally hashed with
code from Ristretto into a uint64. Previously the cache map was map[string]interface{}
now it is map[uint64]interface{}. The cache map is also split into 256 shards now,
with the hopes of reducing lock contention.

There is a (poorly tested) code generator in the gen folder that can create typed caches. To see help
run `go run ./gen/gen.go -h`. As an example this will create a stringCache.go file in a
package named cache that is cache of string (and the item stored in the cache will be called stringZItem):
`go run ./gen/gen.go -o stringCache.go -pkg cache -name stringZ string`. Normally name & the item to be cached
can match, but if caching something like `map[string]interface{}` then the name can't match
the item being cached.

The original version of the below tests have a bug that makes the tests unrealistic and doesn't
let sharding work. This bug has has been fixed in all of the below tests.

patrickmn/go-cache vs Gobd/go-cache
```
CacheGetManyConcurrentExpiring-8     52.6ns ± 5%  16.5ns ± 5%  -68.59%  (p=0.000 n=18+20)
CacheGetManyConcurrentNotExpiring-8  59.2ns ± 2%  10.8ns ± 8%  -81.76%  (p=0.000 n=19+17)
```

patrickmn/go-cache vs patrickmn/go-cache (sharded)
```
CacheGetManyConcurrentExpiring-8     52.6ns ± 5%  31.0ns ±14%  -41.00%  (p=0.000 n=18+19)
CacheGetManyConcurrentNotExpiring-8  59.2ns ± 2%  13.1ns ±14%  -77.85%  (p=0.000 n=19+17)
```

patrickmn/go-cache (sharded) vs Gobd/go-cache
```
CacheGetManyConcurrentExpiring-8     31.0ns ±14%  16.5ns ± 5%  -46.76%  (p=0.000 n=19+20)
CacheGetManyConcurrentNotExpiring-8  13.1ns ±14%  10.8ns ± 8%  -17.66%  (p=0.000 n=17+17)
```

Gobd/go-cache is always faster than the original & the original's sharded implementation.
Allocations aren't shown because they are 0 for all.

### Installation

`go get github.com/Gobd/go-cache`

### Usage

```go
import (
	"fmt"
	"github.com/Gobd/go-cache"
	"time"
)

func main() {
	// Create a cache with a default expiration time of 5 minutes, and which
	// purges expired items every 10 minutes
	c := cache.New(5*time.Minute, 10*time.Minute)

	// Set the value of the key 1234 to "bar", with the default expiration time
	c.SetDefault(1234, "bar")

	// Set the value of the key "baz" to 42, with no expiration time
	// (the item won't be removed until it is re-set, or removed using
	// c.Delete("baz")
	c.Set("baz", 42, cache.NoExpiration)

	// Get the string associated with the key "foo" from the cache
	foo, found := c.Get("foo")
	if found {
		fmt.Println(foo)
	}

	// Since Go is statically typed, and cache values can be anything, type
	// assertion is needed when values are being passed to functions that don't
	// take arbitrary types, (i.e. interface{}). The simplest way to do this for
	// values which will only be used once--e.g. for passing to another
	// function--is:
	foo, found := c.Get(1234)
	if found {
		MyFunction(foo.(string))
	}

	// This gets tedious if the value is used several times in the same function.
	// You might do either of the following instead (or generate a typed cache):
	if x, found := c.Get("foo"); found {
		foo := x.(string)
		// ...
	}
	// or
	var foo string
	if x, found := c.Get("foo"); found {
		foo = x.(string)
	}
	// ...
	// foo can then be passed around freely as a string

	// Want performance? Store pointers!
	c.SetDefault("foo", &MyStruct)
	if x, found := c.Get("foo"); found {
		foo := x.(*MyStruct)
			// ...
	}
}
```

### Reference

`godoc` or [http://godoc.org/github.com/Gobd/go-cache](http://godoc.org/github.com/Gobd/go-cache)
