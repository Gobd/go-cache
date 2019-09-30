package cache

import (
	"fmt"
	"runtime"
	"sync"
	"time"
)

type Item struct {
	Object     interface{}
	Expiration int64
}

// Returns true if the item has expired.
func (item Item) Expired() bool {
	if item.Expiration == 0 {
		return false
	}
	return time.Now().UnixNano() > item.Expiration
}

const (
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
	now               func() time.Time
	items             map[uint64]Item
	mu                sync.RWMutex
	janitor           *janitor
}

type lazyTime struct {
	time   time.Time
	ticker *time.Ticker
}

func (l *lazyTime) now() time.Time {
	select {
	case <-l.ticker.C:
		l.time = time.Now()
		return l.time
	default:
		return l.time
	}
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
		e = c.now().Add(d).UnixNano()
	}
	c.mu.Lock()
	c.items[keyToHash(k)] = Item{
		Object:     x,
		Expiration: e,
	}
	// TODO: Calls to mu.Unlock are currently not deferred because defer
	// adds ~200 ns (as of go1.)
	c.mu.Unlock()
}

func (c *cache) set(k interface{}, x interface{}, d time.Duration) {
	var e int64
	if d == DefaultExpiration {
		d = c.defaultExpiration
	}
	if d > 0 {
		e = c.now().Add(d).UnixNano()
	}
	c.items[keyToHash(k)] = Item{
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
	c.mu.Lock()
	_, found := c.get(k)
	if found {
		c.mu.Unlock()
		return fmt.Errorf("Item %s already exists", k)
	}
	c.set(k, x, d)
	c.mu.Unlock()
	return nil
}

// Set a new value for the cache key only if it already exists, and the existing
// item hasn't expired. Returns an error otherwise.
func (c *cache) Replace(k interface{}, x interface{}, d time.Duration) error {
	c.mu.Lock()
	_, found := c.get(k)
	if !found {
		c.mu.Unlock()
		return fmt.Errorf("Item %s doesn't exist", k)
	}
	c.set(k, x, d)
	c.mu.Unlock()
	return nil
}

// Get an item from the cache. Returns the item or nil, and a bool indicating
// whether the key was found.
func (c *cache) Get(k interface{}) (value interface{}, ok bool) {
	c.mu.RLock()
	// "Inlining" of get and Expired
	item, found := c.items[keyToHash(k)]
	if !found {
		c.mu.RUnlock()
		return
	}
	if item.Expiration > 0 {
		if c.now().UnixNano() > item.Expiration {
			c.mu.RUnlock()
			return
		}
	}
	c.mu.RUnlock()
	return item.Object, true
}

// GetWithExpiration returns an item and its expiration time from the cache.
// It returns the item or nil, the expiration time if one is set (if the item
// never expires a zero value for time.Time is returned), and a bool indicating
// whether the key was found.
func (c *cache) GetWithExpiration(k interface{}) (value interface{}, exp time.Time, ok bool) {
	c.mu.RLock()
	// "Inlining" of get and Expired
	item, found := c.items[keyToHash(k)]
	if !found {
		c.mu.RUnlock()
		return
	}

	if item.Expiration > 0 {
		if c.now().UnixNano() > item.Expiration {
			c.mu.RUnlock()
			return
		}

		// Return the item and the expiration time
		c.mu.RUnlock()
		return item.Object, time.Unix(0, item.Expiration), true
	}

	// If expiration <= 0 (i.e. no expiration time set) then return the item
	// and a zeroed time.Time
	c.mu.RUnlock()
	return item.Object, time.Time{}, true
}

func (c *cache) get(k interface{}) (value interface{}, ok bool) {
	item, found := c.items[keyToHash(k)]
	if !found {
		return
	}
	// "Inlining" of Expired
	if item.Expiration > 0 {
		if c.now().UnixNano() > item.Expiration {
			return
		}
	}
	return item.Object, true
}

// Delete an item from the cache. Does nothing if the key is not in the cache.
func (c *cache) Delete(k interface{}) {
	c.mu.Lock()
	delete(c.items, keyToHash(k))
	c.mu.Unlock()
}

// Delete all expired items from the cache.
func (c *cache) DeleteExpired() {
	now := c.now().UnixNano()
	c.mu.Lock()
	for k, v := range c.items {
		// "Inlining" of expired
		if v.Expiration > 0 && now > v.Expiration {
			delete(c.items, k)
		}
	}
	c.mu.Unlock()
}

// Returns the number of items in the cache. This may include items that have
// expired, but have not yet been cleaned up.
func (c *cache) ItemCount() int {
	c.mu.RLock()
	n := len(c.items)
	c.mu.RUnlock()
	return n
}

// Delete all items from the cache.
func (c *cache) Flush() {
	c.mu.Lock()
	c.items = map[uint64]Item{}
	c.mu.Unlock()
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

func newCache(de time.Duration, ti time.Duration, m map[uint64]Item) *cache {
	if de == 0 {
		de = -1
	}
	c := &cache{
		defaultExpiration: de,
		items:             m,
	}
	if ti <= 0 {
		c.now = time.Now
	} else {
		l := lazyTime{
			time:   time.Now(),
			ticker: time.NewTicker(ti),
		}
		c.now = l.now
	}
	return c
}

func newCacheWithJanitor(de time.Duration, ci time.Duration, ti time.Duration, m map[uint64]Item) *Cache {
	c := newCache(de, ti, m)
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
	items := make(map[uint64]Item)
	return newCacheWithJanitor(defaultExpiration, cleanupInterval, 0, items)
}

// Return a new cache with a given default expiration duration, cleanup,
// and time.Now() interval. This will calculate time.Now() every timeNowInterval
// rather than on every set & get. This means items won't be evicted as quickly
// but results in possibly performance. If the expiration duration is less
// than one, the items in the cache never expire (by default),
// and must be deleted manually. If the cleanup interval is less than one,
// expired items are not deleted from the cache before calling c.DeleteExpired().
func NewLazy(defaultExpiration, cleanupInterval, timeNowInterval time.Duration) *Cache {
	items := make(map[uint64]Item)
	return newCacheWithJanitor(defaultExpiration, cleanupInterval, timeNowInterval, items)
}
