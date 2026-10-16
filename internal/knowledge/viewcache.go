package knowledge

import (
	"fmt"
	"sync"
)

// viewCache keeps the topic views' results (the topic list's evidence, the
// map) between requests: building them reads every claim's evidence, which
// takes seconds at 10⁵ claims (docs/scaling.md). An entry holds while the
// knowledge layer is unchanged (Store.KnowledgeSignature), for the same
// scope and the same day (belief ages with time).
var viewCache = &resultCache{m: map[string]any{}}

type resultCache struct {
	mu sync.Mutex
	m  map[string]any
}

func (c *resultCache) key(v Views, what string) (string, bool) {
	sig, err := v.Store.KnowledgeSignature()
	if err != nil {
		return "", false
	}
	return fmt.Sprintf("%p|%s|%s|%+v|%s", v.Store, what, sig, v.Scope, v.Now.Format("2006-01-02")), true
}

func (c *resultCache) get(v Views, what string) (any, bool) {
	k, ok := c.key(v, what)
	if !ok {
		return nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	x, ok := c.m[k]
	return x, ok
}

func (c *resultCache) put(v Views, what string, x any) {
	k, ok := c.key(v, what)
	if !ok {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.m) >= 16 {
		c.m = map[string]any{} // a few scopes and days at most; start over
	}
	c.m[k] = x
}
