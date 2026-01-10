package yamlstream

import "testing"

func TestParse_Empty(t *testing.T) {
	stream, err := Parse("")
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	if stream == nil {
		t.Fatalf("expected non-nil stream")
	}
	if len(stream.Docs) != 0 {
		t.Fatalf("expected 0 docs, got %d", len(stream.Docs))
	}
	if stream.DocForLine(1) != nil {
		t.Fatalf("expected DocForLine to return nil for empty stream")
	}
}

func TestParse_MultiDocAndDocForLine(t *testing.T) {
	content := "" +
		"apiVersion: v1\n" +
		"kind: ConfigMap\n" +
		"metadata:\n" +
		"  name: cm-one\n" +
		"---\n" +
		"apiVersion: v1\n" +
		"kind: ConfigMap\n" +
		"metadata:\n" +
		"  name: cm-two\n"

	stream, err := Parse(content)
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	if stream == nil {
		t.Fatalf("expected non-nil stream")
	}
	if len(stream.Docs) != 2 {
		t.Fatalf("expected 2 docs, got %d", len(stream.Docs))
	}

	d1 := &stream.Docs[0]
	d2 := &stream.Docs[1]

	if d1.MinLine <= 0 || d1.MaxLine <= 0 || d1.MinLine > d1.MaxLine {
		t.Fatalf("invalid doc1 span: %+v", *d1)
	}
	if d2.MinLine <= 0 || d2.MaxLine <= 0 || d2.MinLine > d2.MaxLine {
		t.Fatalf("invalid doc2 span: %+v", *d2)
	}

	// Pick a line guaranteed to be inside each doc's span.
	if got := stream.DocForLine(d1.MinLine); got == nil || got != d1 {
		t.Fatalf("expected DocForLine(doc1.MinLine) to return doc1")
	}
	if got := stream.DocForLine(d2.MinLine); got == nil || got != d2 {
		t.Fatalf("expected DocForLine(doc2.MinLine) to return doc2")
	}

	// Past the last doc should not match.
	if got := stream.DocForLine(d2.MaxLine + 10); got != nil {
		t.Fatalf("expected DocForLine(outside) to return nil")
	}
}

func TestCache_Get_ReusesSnapshot(t *testing.T) {
	c := NewCache()
	content := "apiVersion: v1\nkind: Pod\nmetadata:\n  name: p\n"

	s1, err := c.Get("file:///a.yaml", 1, content)
	if err != nil {
		t.Fatalf("Get returned error: %v", err)
	}
	s2, err := c.Get("file:///a.yaml", 1, content)
	if err != nil {
		t.Fatalf("Get returned error: %v", err)
	}
	if s1 != s2 {
		t.Fatalf("expected cached stream pointer to be reused")
	}
}

func TestCache_Get_ContentChangeSameVersion_Refreshes(t *testing.T) {
	c := NewCache()

	content1 := "apiVersion: v1\nkind: Pod\nmetadata:\n  name: p1\n"
	content2 := "apiVersion: v1\nkind: Pod\nmetadata:\n  name: p2\n"

	s1, err := c.Get("file:///a.yaml", 1, content1)
	if err != nil {
		t.Fatalf("Get returned error: %v", err)
	}
	s2, err := c.Get("file:///a.yaml", 1, content2)
	if err != nil {
		t.Fatalf("Get returned error: %v", err)
	}
	if s1 == s2 {
		t.Fatalf("expected different stream after content change")
	}
	if len(s2.Docs) != 1 {
		t.Fatalf("expected 1 doc, got %d", len(s2.Docs))
	}
}

func TestCache_InvalidateURI(t *testing.T) {
	c := NewCache()
	content := "apiVersion: v1\nkind: Pod\nmetadata:\n  name: p\n"

	s1, err := c.Get("file:///a.yaml", 1, content)
	if err != nil {
		t.Fatalf("Get returned error: %v", err)
	}
	c.InvalidateURI("file:///a.yaml")
	// After invalidation, the cache should parse again (new pointer).
	s2, err := c.Get("file:///a.yaml", 1, content)
	if err != nil {
		t.Fatalf("Get returned error: %v", err)
	}
	if s1 == s2 {
		t.Fatalf("expected new stream after invalidation")
	}
}
