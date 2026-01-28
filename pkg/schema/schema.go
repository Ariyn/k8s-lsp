package schema

import (
	"fmt"
	"sync"
)

type Node struct {
	Type        Type
	Properties  map[string]*Node
	Items       *Node
	Enum        []string
	Description string
	Default     string
	Nullable    bool
	Ref         *RefMeta

	PreserveUnknownFields bool
	AdditionalProperties  *Node
}

func Any() *Node { return &Node{Type: TypeAny} }

func Obj(props map[string]*Node) *Node {
	return &Node{Type: TypeObject, Properties: props}
}

func Arr(items *Node) *Node {
	return &Node{Type: TypeArray, Items: items}
}

type Registry struct {
	mu    sync.RWMutex
	byGVK map[GVK]*Node
}

func NewRegistry() *Registry {
	return &Registry{byGVK: make(map[GVK]*Node)}
}

func (r *Registry) Set(gvk GVK, n *Node) {
	if r == nil || n == nil || gvk.IsZero() {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.byGVK[gvk] = n
}

func (r *Registry) Get(gvk GVK) *Node {
	if r == nil || gvk.IsZero() {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.byGVK[gvk]
}

func (r *Registry) Count() int {
	if r == nil {
		return 0
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.byGVK)
}

func (n *Node) String() string {
	if n == nil {
		return "<nil>"
	}
	if n.Type != "" && n.Type != TypeAny {
		return string(n.Type)
	}
	if n.Type == TypeObject {
		return "object"
	}
	if n.Type == TypeArray {
		return "array"
	}
	return fmt.Sprintf("schema(%s)", n.Type)
}

func ResolvePath(root *Node, path []string) *Node {
	cur := root
	for _, seg := range path {
		if cur == nil {
			return nil
		}
		// Paths don't include sequence markers; step into items when needed.
		if cur.Type == TypeArray && cur.Items != nil {
			cur = cur.Items
		}
		if cur.Type != TypeObject {
			return nil
		}
		if cur.Properties == nil {
			return nil
		}
		cur = cur.Properties[seg]
	}
	return cur
}

func ResolveParentPath(root *Node, path []string) *Node {
	if len(path) == 0 {
		return root
	}
	return ResolvePath(root, path[:len(path)-1])
}
