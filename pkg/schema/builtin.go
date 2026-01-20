package schema

// Minimal built-in schemas.
//
// Scope: small enough to provide immediate value (unknown key + a few deep completions)
// without bundling the full Kubernetes OpenAPI spec.

func RegisterBuiltins(reg *Registry) {
	if reg == nil {
		return
	}

	// Common ObjectMeta subset.
	objectMeta := Obj(map[string]*Node{
		"name":        {Type: TypeString, Description: "Name must be unique within a namespace."},
		"namespace":   {Type: TypeString, Description: "Namespace defines the space within which each name must be unique."},
		"labels":      {Type: TypeObject, AdditionalProperties: &Node{Type: TypeString}, Description: "Map of string keys and values."},
		"annotations": {Type: TypeObject, AdditionalProperties: &Node{Type: TypeString}, Description: "Map of string keys and values."},
	})

	reg.Set(GVK{Group: "", Version: "__fallback__", Kind: "KubernetesObject"}, KubernetesObjectFallback())

	k8sRoot := func(spec *Node) *Node {
		return Obj(map[string]*Node{
			"apiVersion": {Type: TypeString},
			"kind":       {Type: TypeString},
			"metadata":   objectMeta,
			"spec":       spec,
			"status":     Any(),
		})
	}

	// apps/v1 Deployment: only a thin slice required to make completion useful.
	deployment := k8sRoot(Obj(map[string]*Node{
		"replicas": {Type: TypeInteger, Description: "Number of desired pods."},
		"paused":   {Type: TypeBoolean, Description: "Indicates that the deployment is paused."},
		"selector": Obj(map[string]*Node{
			"matchLabels": {Type: TypeObject, AdditionalProperties: &Node{Type: TypeString}},
		}),
		"template": Obj(map[string]*Node{
			"metadata": objectMeta,
			"spec": Obj(map[string]*Node{
				"restartPolicy": {Type: TypeString, Enum: []string{"Always"}},
				"containers": Arr(Obj(map[string]*Node{
					"name":            {Type: TypeString},
					"image":           {Type: TypeString},
					"imagePullPolicy": {Type: TypeString, Enum: []string{"Always", "IfNotPresent", "Never"}},
					"ports": Arr(Obj(map[string]*Node{
						"containerPort": {Type: TypeInteger},
						"protocol":      {Type: TypeString, Enum: []string{"TCP", "UDP", "SCTP"}},
					})),
				})),
			}),
		}),
	}))
	reg.Set(GVK{Group: "apps", Version: "v1", Kind: "Deployment"}, deployment)
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
