package yamlstream

import (
	"hash/fnv"
	"io"
	"math"
	"strconv"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

type Doc struct {
	Node    *yaml.Node
	MinLine int // 1-based (yaml.v3)
	MaxLine int // 1-based (yaml.v3)
}

type Stream struct {
	Docs []Doc
}

func (s *Stream) DocForLine(line1 int) *Doc {
	if s == nil || len(s.Docs) == 0 {
		return nil
	}
	for i := range s.Docs {
		d := &s.Docs[i]
		if d.MinLine <= line1 && line1 <= d.MaxLine {
			return d
		}
	}
	return nil
}

func Parse(content string) (*Stream, error) {
	decoder := yaml.NewDecoder(strings.NewReader(content))

	var docs []Doc
	for {
		var node yaml.Node
		if err := decoder.Decode(&node); err != nil {
			if err == io.EOF {
				break
			}
			return nil, err
		}
		minLine, maxLine := spanLines(&node)
		docs = append(docs, Doc{Node: &node, MinLine: minLine, MaxLine: maxLine})
	}

	return &Stream{Docs: docs}, nil
}

func spanLines(root *yaml.Node) (int, int) {
	if root == nil {
		return 1, 1
	}
	minLine := int(math.MaxInt)
	maxLine := 0

	var walk func(n *yaml.Node)
	walk = func(n *yaml.Node) {
		if n == nil {
			return
		}
		if n.Line > 0 {
			if n.Line < minLine {
				minLine = n.Line
			}
			if n.Line > maxLine {
				maxLine = n.Line
			}
		}
		for _, c := range n.Content {
			walk(c)
		}
	}
	walk(root)

	if minLine == int(math.MaxInt) {
		minLine = 1
	}
	if maxLine == 0 {
		maxLine = minLine
	}
	return minLine, maxLine
}

func contentHash64(s string) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(s))
	return h.Sum64()
}

type Snapshot struct {
	URI         string
	Version     int32
	ContentHash uint64
	Stream      *Stream
	ParseErr    error
}

type Cache struct {
	mu    sync.RWMutex
	byKey map[string]*Snapshot
}

func NewCache() *Cache {
	return &Cache{byKey: make(map[string]*Snapshot)}
}

func (c *Cache) key(uri string, version int32) string {
	return uri + "\x00" + strconv.FormatInt(int64(version), 10)
}

func (c *Cache) InvalidateURI(uri string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for k := range c.byKey {
		if strings.HasPrefix(k, uri+"\x00") {
			delete(c.byKey, k)
		}
	}
}

func (c *Cache) Get(uri string, version int32, content string) (*Stream, error) {
	if c == nil {
		return Parse(content)
	}
	key := c.key(uri, version)
	h := contentHash64(content)

	c.mu.RLock()
	snap := c.byKey[key]
	c.mu.RUnlock()

	if snap != nil && snap.ContentHash == h {
		return snap.Stream, snap.ParseErr
	}

	stream, err := Parse(content)

	c.mu.Lock()
	c.byKey[key] = &Snapshot{URI: uri, Version: version, ContentHash: h, Stream: stream, ParseErr: err}
	c.mu.Unlock()

	return stream, err
}
