package indexer

import "strings"

// NormalizeNamespace normalizes a Kubernetes namespace.
// Empty/whitespace-only namespaces default to "default".
func NormalizeNamespace(ns string) string {
	if strings.TrimSpace(ns) == "" {
		return "default"
	}
	return ns
}

// IsClusterScopedKind reports whether a Kubernetes Kind is cluster-scoped.
func IsClusterScopedKind(kind string) bool {
	switch kind {
	case "Namespace", "Node", "PersistentVolume", "StorageClass", "ClusterRole", "ClusterRoleBinding", "CustomResourceDefinition":
		return true
	default:
		return false
	}
}

// FormatResourceID formats a search-friendly, consistent identifier for a Kubernetes resource.
// Namespaced kinds: "Kind namespace/name".
// Cluster-scoped kinds (or empty namespace): "Kind name".
func FormatResourceID(kind, namespace, name string) string {
	kind = strings.TrimSpace(kind)
	name = strings.TrimSpace(name)
	namespace = strings.TrimSpace(namespace)
	if kind == "" {
		return name
	}
	if name == "" {
		return kind
	}

	if IsClusterScopedKind(kind) {
		return kind + " " + name
	}
	ns := NormalizeNamespace(namespace)
	return kind + " " + ns + "/" + name
}
