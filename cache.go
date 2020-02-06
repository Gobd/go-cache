package cache

import (
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
func (c *cache) Set(k string, x interface{}, d time.Duration) {
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
	c.items[idx].Unlock()
}

// Add an item to the cache, replacing any existing item, using the default
// expiration.
func (c *cache) SetDefault(k string, x interface{}) {
	c.Set(k, x, c.defaultExpiration)
}

// Get an item from the cache. Returns the item or nil, and a bool indicating
// whether the key was found.
func (c *cache) Get(k string) (value interface{}, ok bool) {
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

// Delete an item from the cache. Does nothing if the key is not in the cache.
func (c *cache) Delete(k string) {
	key := keyToHash(k)
	idx := key % numShards
	c.items[idx].Lock()
	delete(c.items[idx].data, key)
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

// memhash is the hash function used by go map, it utilizes available hardware instructions
// (behaves as aeshash if aes instruction is available).
// NOTE: The hash seed changes for every process. So, this cannot be used as a persistent hash.

// keyToHash interprets the type of key and converts it to a uint64 hash.
func keyToHash(key string) uint64 {
	ss := (*stringStruct)(unsafe.Pointer(&key))
	return uint64(memhash(ss.str, 0, uintptr(ss.len)))
}
