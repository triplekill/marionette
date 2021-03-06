package modules

import "sync"

// This is a map of known modules.
var handlers = struct {
	m map[string]TestCtor
	sync.RWMutex
}{m: make(map[string]TestCtor)}

// TestCtor is the signature of a constructor-function.
type TestCtor func() ModuleAPI

// Register records a new module.
func Register(id string, newfunc TestCtor) {
	handlers.Lock()
	handlers.m[id] = newfunc
	handlers.Unlock()
}

// Lookup is the factory-method which looks up and returns
// an object of the given type - if possible.
func Lookup(id string) (a ModuleAPI) {
	handlers.RLock()
	ctor, ok := handlers.m[id]
	handlers.RUnlock()
	if ok {
		a = ctor()
	}
	return
}

// Modules returns the names of all the registered module-names.
func Modules() []string {
	var result []string

	// For each handler save the name
	handlers.RLock()
	for index := range handlers.m {
		result = append(result, index)
	}
	handlers.RUnlock()

	// And return the result
	return result

}
