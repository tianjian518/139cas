package doubao_new

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/OpenListTeam/OpenList/v4/drivers/base"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
)

// linkCacheMax bounds how many signed stream URLs are remembered. Doubao answers
// a signed URL for well over an hour, and every seek otherwise re-derives one:
// a JWT refresh, a DPoP key derivation and an ES256 signature. With a 12 GB
// file that is one link per episode, so a few hundred entries covers a whole
// library.
const linkCacheMax = 512

// linkCacheTTL is deliberately far below the observed URL lifetime (still valid
// past six minutes), leaving room for clock skew without ever handing out a
// URL that is about to lapse.
const linkCacheTTL = 30 * time.Minute

// inflightTTL bounds how long a single sign stays shared with concurrent
// callers. A sign takes at most a few seconds; anything older means the owner
// died mid-flight and the next caller should sign again.
const inflightTTL = 2 * time.Minute

type cachedLink struct {
	url     string
	referer string
	expiry  time.Time
}

type inflightLink struct {
	done   chan struct{}
	expiry time.Time
}

type linkCache struct {
	mu       sync.Mutex
	entries  map[string]cachedLink
	inflight map[string]*inflightLink
}

func newLinkCache() *linkCache {
	return &linkCache{
		entries:  make(map[string]cachedLink, linkCacheMax),
		inflight: make(map[string]*inflightLink, 16),
	}
}

func (c *linkCache) get(key string) (cachedLink, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok {
		return cachedLink{}, false
	}
	if time.Now().After(e.expiry) {
		delete(c.entries, key)
		return cachedLink{}, false
	}
	return e, true
}

func (c *linkCache) put(key, url, referer string) {
	if url == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.entries) >= linkCacheMax {
		now := time.Now()
		var oldestKey string
		var oldest time.Time
		for k, v := range c.entries {
			if now.After(v.expiry) {
				delete(c.entries, k)
				continue
			}
			if oldestKey == "" || v.expiry.Before(oldest) {
				oldestKey, oldest = k, v.expiry
			}
		}
		if len(c.entries) >= linkCacheMax && oldestKey != "" {
			delete(c.entries, oldestKey)
		}
	}
	c.entries[key] = cachedLink{url: url, referer: referer, expiry: time.Now().Add(linkCacheTTL)}
}

func (c *linkCache) drop(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.entries, key)
}

func (c *linkCache) dropAll() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = make(map[string]cachedLink, linkCacheMax)
}

// begin claims the right to sign key. The owner gets a non-nil done channel to
// close when it is finished; a second caller gets nil for done and a channel
// that closes when the owner is done, so it can wait instead of signing too.
func (c *linkCache) begin(key string) (done, wait chan struct{}) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if f, ok := c.inflight[key]; ok {
		if time.Now().Before(f.expiry) {
			return nil, f.done
		}
	}
	f := &inflightLink{done: make(chan struct{}), expiry: time.Now().Add(inflightTTL)}
	c.inflight[key] = f
	return f.done, nil
}

func (c *linkCache) end(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if f, ok := c.inflight[key]; ok {
		delete(c.inflight, key)
		close(f.done)
	}
}

// linkPreWarmer signs the files of a directory in the background right after it
// has been listed, so the first Range request of the episode the user picked is
// answered from the cache. It signs only — no media bytes are read — so the
// server's bandwidth is untouched.
type linkPreWarmer struct {
	mu    sync.Mutex
	ttl   time.Duration
	seen  map[string]time.Time
	slots chan struct{}
}

const (
	prewarmTTL         = 10 * time.Minute
	prewarmSeenMax     = 4096
	prewarmConcurrency = 4
)

func newLinkPreWarmer() *linkPreWarmer {
	return &linkPreWarmer{
		ttl:   prewarmTTL,
		seen:  make(map[string]time.Time, 64),
		slots: make(chan struct{}, prewarmConcurrency),
	}
}

func (p *linkPreWarmer) claim(key string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := time.Now()
	if exp, ok := p.seen[key]; ok && now.Before(exp) {
		return false
	}
	if len(p.seen) >= prewarmSeenMax {
		for k, exp := range p.seen {
			if now.After(exp) {
				delete(p.seen, k)
			}
		}
		if len(p.seen) >= prewarmSeenMax {
			p.seen = make(map[string]time.Time, 64)
		}
	}
	p.seen[key] = now.Add(p.ttl)
	return true
}

func (p *linkPreWarmer) release(key string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.seen, key)
}

func (d *DoubaoNew) warmObjects(objs []model.Obj) {
	for _, obj := range objs {
		if obj.IsDir() {
			continue
		}
		go d.warmObject(obj)
	}
}

// warmObject signs one file's link.
func (d *DoubaoNew) warmObject(obj model.Obj) {
	o, ok := obj.(*Object)
	if !ok || o.ObjToken == "" {
		return
	}
	cacheKey := o.ObjToken
	if d.ShareLink {
		cacheKey = "share:" + cacheKey
	}
	if _, ok := d.linkCacheFor().get(cacheKey); ok {
		return
	}
	warmer := d.prewarmerFor()
	if !warmer.claim(cacheKey) {
		return
	}
	warmer.slots <- struct{}{}
	defer func() { <-warmer.slots }()

	// Sign on a detached context: the player may cancel the request that
	// triggered the listing at any moment, but the link is still worth having.
	signCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := d.feishuLink(signCtx, o, model.LinkArgs{}); err != nil {
		warmer.release(cacheKey)
	}
}

// warmFallbackObjects does the same for samantha-listed files. They carry no
// feishu obj token, so the warm path is a different request and the cache key
// is the samantha node.
func (d *DoubaoNew) warmFallbackObjects(objs []model.Obj) {
	for _, obj := range objs {
		if obj.IsDir() {
			continue
		}
		go d.warmFallbackObject(obj)
	}
}

func (d *DoubaoNew) warmFallbackObject(obj model.Obj) {
	o, ok := obj.(*Object)
	if !ok || o.ObjToken != "" || o.GetID() == "" {
		return
	}
	cacheKey := fallbackCacheKey(o.GetID())
	if _, ok := d.linkCacheFor().get(cacheKey); ok {
		return
	}
	warmer := d.prewarmerFor()
	if !warmer.claim(cacheKey) {
		return
	}
	warmer.slots <- struct{}{}
	defer func() { <-warmer.slots }()

	signCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	link, err := d.fallbackLink(signCtx, o)
	if err != nil || link == nil || link.URL == "" {
		warmer.release(cacheKey)
		return
	}
	referer := ""
	if v := link.Header.Get("Referer"); v != "" {
		referer = v
	}
	if referer == "" {
		referer = DoubaoURL + "/"
	}
	d.linkCacheFor().put(cacheKey, link.URL, referer)
}

func fallbackCacheKey(nodeID string) string {
	return "samantha:" + nodeID
}

func (d *DoubaoNew) prewarmerFor() *linkPreWarmer {
	d.linksMu.Lock()
	defer d.linksMu.Unlock()
	if d.prewarmer == nil {
		d.prewarmer = newLinkPreWarmer()
	}
	return d.prewarmer
}

func linkHeaders(referer string) http.Header {
	return http.Header{
		"Referer":    []string{referer},
		"User-Agent": []string{base.UserAgent},
	}
}
