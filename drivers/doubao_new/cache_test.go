package doubao_new

import (
	"sync"
	"testing"
	"time"
)

func TestLinkCacheExpiryAndEviction(t *testing.T) {
	c := newLinkCache()
	c.put("a", "http://a", "http://ref")
	if e, ok := c.get("a"); !ok || e.url != "http://a" {
		t.Fatalf("expected a cached url for a, got %v %v", e.url, ok)
	}

	c.mu.Lock()
	c.entries["a"] = cachedLink{url: "http://a", expiry: time.Now().Add(-time.Second)}
	c.mu.Unlock()
	if _, ok := c.get("a"); ok {
		t.Fatal("expired entry should not be returned")
	}

	for i := 0; i < linkCacheMax+10; i++ {
		c.put(string(rune('0'+i%10))+time.Duration(i).String(), "u", "r")
	}
	c.mu.Lock()
	n := len(c.entries)
	c.mu.Unlock()
	if n > linkCacheMax {
		t.Fatalf("cache grew past its bound: %d", n)
	}
}

func TestLinkCacheDedupesConcurrentSigns(t *testing.T) {
	c := newLinkCache()
	done, wait := c.begin("k")
	if done == nil || wait != nil {
		t.Fatalf("first caller should own the sign: done=%v wait=%v", done, wait)
	}
	otherDone, otherWait := c.begin("k")
	if otherDone != nil || otherWait == nil {
		t.Fatalf("second caller should wait: done=%v wait=%v", otherDone, otherWait)
	}

	finished := make(chan struct{})
	go func() {
		<-otherWait
		close(finished)
	}()
	c.end("k")
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("waiter should be released when the owner finishes")
	}

	third, _ := c.begin("k")
	if third == nil {
		t.Fatal("after the owner finished a new caller should own the sign")
	}
	c.end("k")
}

func TestPreWarmerSuppressesRepeats(t *testing.T) {
	p := newLinkPreWarmer()
	if !p.claim("x") {
		t.Fatal("first claim should succeed")
	}
	if p.claim("x") {
		t.Fatal("a second claim inside the ttl should be suppressed")
	}
	p.release("x")
	if !p.claim("x") {
		t.Fatal("a released key should be claimable again")
	}
}

func TestDirPageCacheHonoursDirty(t *testing.T) {
	c := newDirPageCache()
	c.put("root", "", dirPage{nodes: []Node{{NodeToken: "n1", Name: "a"}}, last: "cursor"})
	if p, ok := c.get("root", ""); !ok || len(p.nodes) != 1 || p.last != "cursor" {
		t.Fatalf("expected the page back, got %+v %v", p, ok)
	}

	c.invalidate("root")
	if _, ok := c.get("root", ""); ok {
		t.Fatal("a dirty directory must not serve cached pages")
	}

	c.mu.Lock()
	delete(c.dirty, "root")
	c.mu.Unlock()
	if _, ok := c.get("root", ""); ok {
		t.Fatal("invalidate should also have removed the entries")
	}
}

func TestDirPageCacheRoundTripThroughNodes(t *testing.T) {
	nodes := []Node{{NodeToken: "n1", Name: "a"}, {NodeToken: "n2", Name: "b"}}
	ents := entitiesFromNodes(nodes)
	data := ListData{Entities: ents, NodeList: nodeTokens(nodes)}
	back := nodesOfEntities(data)
	if len(back) != 2 || back[0].Name != "a" || back[1].Name != "b" {
		t.Fatalf("round trip lost data: %+v", back)
	}
}

func TestLinkCacheConcurrentUse(t *testing.T) {
	c := newLinkCache()
	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			k := "k"
			if done, wait := c.begin(k); done == nil {
				<-wait
				c.get(k)
				return
			}
			time.Sleep(time.Millisecond)
			c.put(k, "u", "r")
			c.end(k)
			c.get(k)
			c.drop(k)
		}(i)
	}
	wg.Wait()
}
