package indexer

import (
	"sync"

	"github.com/rs/zerolog/log"
)

type Reference struct {
	Kind      string // Optional, if known
	Name      string // The value of the reference
	Namespace string // Optional
	Symbol    string // The symbol name (e.g. "k8s.resource.name")
	Line      int
	Col       int
}

type K8sResource struct {
	ApiVersion string
	Kind       string
	Name       string
	Namespace  string
	Labels     map[string]string
	References []Reference
	FilePath   string
	Line       int // 0-based line number
	Col        int // 0-based column number
}

type Store struct {
	resources map[string]*K8sResource // Key: "Kind/Namespace/Name"
	mu        sync.RWMutex
}

func NewStore() *Store {
	return &Store{
		resources: make(map[string]*K8sResource),
	}
}

// makeKey generates a unique key for the resource.
// Format: Kind/Namespace/Name
// If namespace is empty, it defaults to "default".
func makeKey(kind, namespace, name string) string {
	if namespace == "" {
		namespace = "default"
	}
	return kind + "/" + namespace + "/" + name
}

func (s *Store) Add(res *K8sResource) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := makeKey(res.Kind, res.Namespace, res.Name)
	log.Debug().Str("key", key).Msg("Adding resource to store")
	s.resources[key] = res
}

func (s *Store) Get(kind, namespace, name string) *K8sResource {
	s.mu.RLock()
	defer s.mu.RUnlock()
	key := makeKey(kind, namespace, name)
	log.Debug().Str("key", key).Msg("Getting resource from store")
	return s.resources[key]
}

func (s *Store) FindByLabel(key, value string) []*K8sResource {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var results []*K8sResource
	for _, res := range s.resources {
		if val, ok := res.Labels[key]; ok && val == value {
			results = append(results, res)
		}
	}
	return results
}

func (s *Store) FindReferences(kind, name string) []*K8sResource {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var results []*K8sResource
	for _, res := range s.resources {
		for _, ref := range res.References {
			if ref.Kind == kind && ref.Name == name {
				results = append(results, res)
				// Break inner loop to avoid adding same resource multiple times if it references same target multiple times
				break
			}
		}
	}
	return results
}

func (s *Store) FindLabelReferences(value string) []*K8sResource {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var results []*K8sResource
	for _, res := range s.resources {
		for _, ref := range res.References {
			if ref.Symbol == "k8s.label" && ref.Name == value {
				results = append(results, res)
				break
			}
		}
	}
	return results
}
