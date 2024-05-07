// https://stackoverflow.com/a/25487392
package alert

import (
	"sync"
	"time"
)

type item struct {
    value      int
    lastAccess int64
}

type TTLMap struct {
    m map[string]*item
    l sync.Mutex
}

func NewTTLMap(ln int, maxTTL int) (m *TTLMap) {
    m = &TTLMap{m: make(map[string]*item, ln)}
    go func() {
        for now := range time.Tick(time.Second) {
            m.l.Lock()
            for k, v := range m.m {
                if now.Unix() - v.lastAccess > int64(maxTTL) {
                    delete(m.m, k)
                }
            }
            m.l.Unlock()
        }
    }()
    return
}

func (m *TTLMap) Len() int {
    return len(m.m)
}

func (m *TTLMap) Put(k string, v int) {
    m.l.Lock()
    it, ok := m.m[k]
    if !ok {
        it = &item{value: v}
        m.m[k] = it
    }
    it.lastAccess = time.Now().Unix()
    m.l.Unlock()
}

func (m *TTLMap) Get(k string) (v int) {
    m.l.Lock()
    if it, ok := m.m[k]; ok {
        v = it.value
        it.lastAccess = time.Now().Unix()
    }
    m.l.Unlock()
    return
}
