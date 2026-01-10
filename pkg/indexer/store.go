package indexer

import (
	"sync"

	"github.com/rs/zerolog/log"
)

type Reference struct {
	Kind      string // Optional, if known
	Name      string // The value of the reference
	Key       string // Optional sub-key (e.g. ConfigMap data key)
	Namespace string // Optional
	Symbol    string // The symbol name (e.g. "k8s.resource.name")
	Line      int
	Col       int
}

type LabelDefinition struct {
	Key   string
	Value string
	Line  int
	Col   int
}

type K8sResource struct {
	ApiVersion string
	Kind       string
	Name       string
	Namespace  string
	Labels     map[string]string
	LabelDefs  []LabelDefinition
	References []Reference
	FilePath   string
	Line       int // 0-based line number
	Col        int // 0-based column number
}

type Store struct {
	resources  map[string]*K8sResource          // Key: "Kind/Namespace/Name"
	keysByFile map[string]map[string]struct{}  // filePath -> set(resourceKey)
	mu         sync.RWMutex
}

func NewStore() *Store {
	return &Store{
		resources:  make(map[string]*K8sResource),
		keysByFile: make(map[string]map[string]struct{}),
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

	// Keep file -> resourceKey reverse index in sync.
	// If an existing resource key moves between files (or file path changes), update the mapping.
	if prev, ok := s.resources[key]; ok && prev != nil {
		prevPath := prev.FilePath
		if prevPath != "" && prevPath != res.FilePath {
			if set, ok := s.keysByFile[prevPath]; ok {
				delete(set, key)
				if len(set) == 0 {
					delete(s.keysByFile, prevPath)
				}
			}
		}
	}

	if res.FilePath != "" {
		set, ok := s.keysByFile[res.FilePath]
		if !ok {
			set = make(map[string]struct{})
			s.keysByFile[res.FilePath] = set
		}
		set[key] = struct{}{}
	}

	s.resources[key] = res
}

// RemoveByFilePath removes all resources that were indexed from the given file path.
// Returns the number of removed resources.
func (s *Store) RemoveByFilePath(path string) int {
	if path == "" {
		return 0
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	set, ok := s.keysByFile[path]
	if !ok || len(set) == 0 {
		delete(s.keysByFile, path)
		return 0
	}

	removed := 0
	for key := range set {
		if _, exists := s.resources[key]; exists {
			delete(s.resources, key)
			removed++
		}
	}
	delete(s.keysByFile, path)
	return removed
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

// FindLabelReferencesByKeyValue returns resources that reference a label selector key/value.
// Backward compatible: if stored references have empty Key, matches by value only.
func (s *Store) FindLabelReferencesByKeyValue(key, value string) []*K8sResource {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var results []*K8sResource
	for _, res := range s.resources {
		for _, ref := range res.References {
			if ref.Symbol != "k8s.label" {
				continue
			}
			if ref.Name != value {
				continue
			}
			if ref.Key != "" && ref.Key != key {
				continue
			}
			results = append(results, res)
			break
		}
	}
	return results
}

func (s *Store) ListByKind(kind string) []*K8sResource {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var results []*K8sResource
	for _, res := range s.resources {
		if res.Kind == kind {
			results = append(results, res)
		}
	}
	return results
}
