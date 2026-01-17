package schema

// Minimal built-in schemas.
//
// Scope: small enough to provide immediate value (unknown key + a few deep completions)
// without bundling the full Kubernetes OpenAPI spec.

func RegisterBuiltins(reg *Registry) {
	if reg == nil {
		return
	}

	// Minimal fallback schema. Detailed schemas for built-in resources are provided
	// via schema packs (schemas/*.yaml next to the server binary) or schemaSources.
	reg.Set(GVK{Group: "", Version: "__fallback__", Kind: "KubernetesObject"}, KubernetesObjectFallback())
}

// KubernetesObjectFallback returns a generic schema for Kubernetes objects.
//
// It is used when the document looks like a Kubernetes resource (has apiVersion+kind)
// but we don't have a specific GVK schema.
func KubernetesObjectFallback() *Node {
	objectMeta := Obj(map[string]*Node{
		"name":        {Type: TypeString, Description: "Name must be unique within a namespace."},
		"namespace":   {Type: TypeString, Description: "Namespace defines the space within which each name must be unique."},
		"labels":      {Type: TypeObject, AdditionalProperties: &Node{Type: TypeString}},
		"annotations": {Type: TypeObject, AdditionalProperties: &Node{Type: TypeString}},
	})
	return Obj(map[string]*Node{
		"apiVersion": {Type: TypeString},
		"kind":       {Type: TypeString},
		"metadata":   objectMeta,
		"spec":       Any(),
		"status":     Any(),
	})
}
