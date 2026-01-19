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

	// v1 Service: minimal spec fields + enum completion.
	service := k8sRoot(Obj(map[string]*Node{
		"type":     {Type: TypeString, Enum: []string{"ClusterIP", "NodePort", "LoadBalancer", "ExternalName"}},
		"selector": {Type: TypeObject, AdditionalProperties: &Node{Type: TypeString}},
		"ports": Arr(Obj(map[string]*Node{
			"port":       {Type: TypeInteger},
			"targetPort": {Type: TypeAny, Description: "Int or string."},
			"protocol":   {Type: TypeString, Enum: []string{"TCP", "UDP", "SCTP"}},
			"name":       {Type: TypeString},
		})),
	}))
	reg.Set(GVK{Group: "", Version: "v1", Kind: "Service"}, service)

	// v1 ConfigMap: useful for basic unknown key + data typing.
	configMap := k8sRoot(Obj(map[string]*Node{
		"data":       {Type: TypeObject, AdditionalProperties: &Node{Type: TypeString}},
		"binaryData": {Type: TypeObject, AdditionalProperties: &Node{Type: TypeString}},
		"immutable":  {Type: TypeBoolean},
	}))
	reg.Set(GVK{Group: "", Version: "v1", Kind: "ConfigMap"}, configMap)

	// v1 Secret: minimal.
	secret := k8sRoot(Obj(map[string]*Node{
		"type":       {Type: TypeString},
		"data":       {Type: TypeObject, AdditionalProperties: &Node{Type: TypeString}},
		"stringData": {Type: TypeObject, AdditionalProperties: &Node{Type: TypeString}},
		"immutable":  {Type: TypeBoolean},
	}))
	reg.Set(GVK{Group: "", Version: "v1", Kind: "Secret"}, secret)
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
