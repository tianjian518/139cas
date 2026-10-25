package doubao_new

import (
	"sync"
	"time"
)

// dirPageTTL is how long one page of a directory listing is reused. Doubao has
// no cheap way to ask whether a folder changed, and the folder the user is
// browsing is usually local to the server, so a few minutes of staleness is the
// right trade against re-walking the whole tree.
const dirPageTTL = 5 * time.Minute

// dirPageCacheMax caps the pages kept, across all directories.
const dirPageCacheMax = 2048

// dirPage is one page of children/list.
type dirPage struct {
	nodes   []Node
	hasMore bool
	last    string
	expiry  time.Time
}

// dirPageCache remembers pages by (parent token, labelled cursor).
//
// children/list is sequential and costs ~0.5-2 s per page. A folder holding a
// whole series therefore takes several seconds to list, and OpenList needs the
// listing of every ancestor before it can resolve a deep path — that walk is
// what made the first seek after a restart take ten seconds. Caching the pages
// turns every repeat walk (navigating deeper, a second player seeking) into a
// map lookup.
type dirPageCache struct {
	mu      sync.Mutex
	entries map[string]dirPage
	// invalidation marks directories known to have changed on our side (a
	// write or a delete went through). Pages of such a directory are served
	// only from a fresh fetch until the parent is re-listed.
	dirty map[string]time.Time
}

func newDirPageCache() *dirPageCache {
	return &dirPageCache{
		entries: make(map[string]dirPage, 64),
		dirty:   make(map[string]time.Time, 8),
	}
}

func dirPageKey(parent, label string) string {
	return parent + "\x00" + label
}

func (c *dirPageCache) get(parent, label string) (dirPage, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if exp, ok := c.dirty[parent]; ok && time.Now().Before(exp) {
		return dirPage{}, false
	}
	p, ok := c.entries[dirPageKey(parent, label)]
	if !ok {
		return dirPage{}, false
	}
	if time.Now().After(p.expiry) {
		delete(c.entries, dirPageKey(parent, label))
		return dirPage{}, false
	}
	return p, true
}

func (c *dirPageCache) put(parent, label string, p dirPage) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.entries) >= dirPageCacheMax {
		now := time.Now()
		for k, v := range c.entries {
			if now.After(v.expiry) {
				delete(c.entries, k)
			}
		}
		if len(c.entries) >= dirPageCacheMax {
			c.entries = make(map[string]dirPage, 64)
		}
	}
	p.expiry = time.Now().Add(dirPageTTL)
	c.entries[dirPageKey(parent, label)] = p
}

// invalidate drops every page of a directory after a write through this driver,
// so the next listing reflects it instead of waiting out the TTL.
func (c *dirPageCache) invalidate(parent string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	prefix := parent + "\x00"
	for k := range c.entries {
		if len(k) >= len(prefix) && k[:len(prefix)] == prefix {
			delete(c.entries, k)
		}
	}
	c.dirty[parent] = time.Now().Add(dirPageTTL)
}

func (d *DoubaoNew) pageCache() *dirPageCache {
	d.linksMu.Lock()
	defer d.linksMu.Unlock()
	if d.pages == nil {
		d.pages = newDirPageCache()
	}
	return d.pages
}
