package cache

import (
	"fmt"
	"runtime"
	"sync"
	"time"
	"unsafe"
)

type lockedMap struct {
	sync.RWMutex
	data map[uint64]item
}

type item struct {
	Object     interface{}
	Expiration int64
}

// Returns true if the item has expired.
func (item item) Expired() bool {
	if item.Expiration == 0 {
		return false
	}
	return nanoTime() > item.Expiration
}

const (
	numShards uint64 = 256
	// For use with functions that take an expiration time.
	NoExpiration time.Duration = -1
	// For use with functions that take an expiration time. Equivalent to
	// passing in the same expiration duration as was given to New() or
	// NewFrom() when the cache was created (e.g. 5 minutes.)
	DefaultExpiration time.Duration = 0
)

type Cache struct {
	*cache
	// If this is confusing, see the comment at the bottom of New()
}

type cache struct {
	defaultExpiration time.Duration
	items             []*lockedMap
	janitor           *janitor
}

// Add an item to the cache, replacing any existing item. If the duration is -1,
// the item never expires. Prefer SetDefault.
func (c *cache) Set(k interface{}, x interface{}, d time.Duration) {
	// "Inlining" of set
	var e int64
	if d == DefaultExpiration {
		d = c.defaultExpiration
	}
	if d > 0 {
		e = nanoTime() + d.Nanoseconds()
	}
	key := keyToHash(k)
	idx := key % numShards
	c.items[idx].Lock()
	c.items[idx].data[key] = item{
		Object:     x,
		Expiration: e,
	}
	// TODO: Calls to mu.Unlock are currently not deferred because defer
	// adds ~200 ns (as of go1.)
	c.items[idx].Unlock()
}

func (c *cache) set(k interface{}, x interface{}, d time.Duration) {
	var e int64
	if d == DefaultExpiration {
		d = c.defaultExpiration
	}
	if d > 0 {
		e = nanoTime() + d.Nanoseconds()
	}
	key := keyToHash(k)
	idx := key % numShards
	c.items[idx].data[keyToHash(k)] = item{
		Object:     x,
		Expiration: e,
	}
}

// Add an item to the cache, replacing any existing item, using the default
// expiration.
func (c *cache) SetDefault(k interface{}, x interface{}) {
	c.Set(k, x, c.defaultExpiration)
}

// Add an item to the cache only if an item doesn't already exist for the given
// key, or if the existing item has expired. Returns an error otherwise.
func (c *cache) Add(k interface{}, x interface{}, d time.Duration) error {
	key := keyToHash(k)
	idx := key % numShards
	c.items[idx].Lock()
	_, found := c.get(k)
	if found {
		c.items[idx].Unlock()
		return fmt.Errorf("item %s already exists", k)
	}
	c.set(k, x, d)
	c.items[idx].Unlock()
	return nil
}

// Get an item from the cache. Returns the item or nil, and a bool indicating
// whether the key was found.
func (c *cache) Get(k interface{}) (value interface{}, ok bool) {
	key := keyToHash(k)
	idx := key % numShards
	c.items[idx].RLock()
	// "Inlining" of get and Expired
	item, found := c.items[idx].data[key]
	if !found {
		c.items[idx].RUnlock()
		return
	}
	if item.Expiration > 0 {
		if nanoTime() > item.Expiration {
			c.items[idx].RUnlock()
			return
		}
	}
	c.items[idx].RUnlock()
	return item.Object, true
}

// GetWithExpiration returns an item and its expiration time from the cache.
// It returns the item or nil, the expiration time if one is set (if the item
// never expires a zero value for time.Time is returned), and a bool indicating
// whether the key was found.
func (c *cache) GetWithExpiration(k interface{}) (value interface{}, exp time.Time, ok bool) {
	key := keyToHash(k)
	idx := key % numShards
	c.items[idx].RLock()
	// "Inlining" of get and Expired
	item, found := c.items[idx].data[key]
	if !found {
		c.items[idx].RUnlock()
		return
	}

	if item.Expiration > 0 {
		if nanoTime() > item.Expiration {
			c.items[idx].RUnlock()
			return
		}

		// Return the item and the expiration time
		c.items[idx].RUnlock()
		return item.Object, time.Unix(0, item.Expiration), true
	}

	// If expiration <= 0 (i.e. no expiration time set) then return the item
	// and a zeroed time.Time
	c.items[idx].RUnlock()
	return item.Object, time.Time{}, true
}

func (c *cache) get(k interface{}) (value interface{}, ok bool) {
	key := keyToHash(k)
	idx := key % numShards

	item, found := c.items[idx].data[key]
	if !found {
		return
	}
	// "Inlining" of Expired
	if item.Expiration > 0 {
		if nanoTime() > item.Expiration {
			return
		}
	}
	return item.Object, true
}

// Delete an item from the cache. Does nothing if the key is not in the cache.
func (c *cache) Delete(k interface{}) {
	key := keyToHash(k)
	idx := key % numShards
	c.items[idx].Lock()
	delete(c.items[idx].data, keyToHash(k))
	c.items[idx].Unlock()
}

// Delete all expired items from the cache.
func (c *cache) DeleteExpired() {
	now := nanoTime()
	for i := range c.items {
		c.items[i].Lock()
		for j := range c.items[i].data {
			// "Inlining" of expired
			if c.items[i].data[j].Expiration > 0 && now > c.items[i].data[j].Expiration {
				delete(c.items[i].data, j)
			}
		}
		c.items[i].Unlock()
	}
}

// Returns the number of items in the cache. This may include items that have
// expired, but have not yet been cleaned up.
func (c *cache) ItemCount() int {
	var n int
	for i := range c.items {
		c.items[i].RLock()
		n += len(c.items[i].data)
		c.items[i].RUnlock()
	}
	return n
}

// Delete all items from the cache.
func (c *cache) Flush() {
	for i := range c.items {
		c.items[i].Lock()
		c.items[i].data = make(map[uint64]item)
		c.items[i].Unlock()
	}
}

type janitor struct {
	Interval time.Duration
	stop     chan bool
}

func (j *janitor) Run(c *cache) {
	ticker := time.NewTicker(j.Interval)
	for {
		select {
		case <-ticker.C:
			c.DeleteExpired()
		case <-j.stop:
			ticker.Stop()
			return
		}
	}
}

func stopJanitor(c *Cache) {
	c.janitor.stop <- true
}

func runJanitor(c *cache, ci time.Duration) {
	j := &janitor{
		Interval: ci,
		stop:     make(chan bool),
	}
	c.janitor = j
	go j.Run(c)
}

func newCache(de time.Duration) *cache {
	if de == 0 {
		de = -1
	}
	c := &cache{
		defaultExpiration: de,
	}
	sm := make([]*lockedMap, int(numShards))
	for i := range sm {
		sm[i] = &lockedMap{data: make(map[uint64]item)}
	}
	c.items = sm
	return c
}

func newCacheWithJanitor(de time.Duration, ci time.Duration) *Cache {
	c := newCache(de)
	// This trick ensures that the janitor goroutine (which--granted it
	// was enabled--is running DeleteExpired on c forever) does not keep
	// the returned C object from being garbage collected. When it is
	// garbage collected, the finalizer stops the janitor goroutine, after
	// which c can be collected.
	C := &Cache{c}
	if ci > 0 {
		runJanitor(c, ci)
		runtime.SetFinalizer(C, stopJanitor)
	}
	return C
}

// Return a new cache with a given default expiration duration and cleanup
// interval. If the expiration duration is less than one,
// the items in the cache never expire (by default), and must be deleted
// manually. If the cleanup interval is less than one, expired items are not
// deleted from the cache before calling c.DeleteExpired().
func New(defaultExpiration, cleanupInterval time.Duration) *Cache {
	return newCacheWithJanitor(defaultExpiration, cleanupInterval)
}

// functions below taken from https://github.com/dgraph-io/ristretto

// nanoTime returns the current time in nanoseconds from a monotonic clock.
//go:linkname nanoTime runtime.nanotime
func nanoTime() int64

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
