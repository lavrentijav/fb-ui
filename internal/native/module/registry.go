package module

import (
	"fmt"
	"sync"
)

var (
	mu   sync.Mutex
	mods []Module
)

// Register adds a module; modules call it from init in a file guarded by their
// build tag, so a tagged build never contains them.
func Register(m Module) {
	mu.Lock()
	defer mu.Unlock()
	for _, have := range mods {
		if have.Name() == m.Name() {
			panic(fmt.Sprintf("module %q registered twice", m.Name()))
		}
	}
	mods = append(mods, m)
}

// All returns the registered modules in registration order.
func All() []Module {
	mu.Lock()
	defer mu.Unlock()
	return append([]Module(nil), mods...)
}

// Reset forgets every module; tests only.
func Reset() {
	mu.Lock()
	defer mu.Unlock()
	mods = nil
}
