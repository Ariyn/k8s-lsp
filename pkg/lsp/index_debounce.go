package lsp

import "time"

func scheduleIndexUpdate(uri string, path string) {
	if uri == "" || path == "" {
		return
	}
	if state == nil || state.Indexer == nil || state.Store == nil {
		return
	}
	content, _, ok := state.getDocument(uri)
	if !ok {
		return
	}

	debounce := state.IndexDebounce
	if debounce < 0 {
		debounce = 0
	}

	state.indexMu.Lock()
	if state.indexTimers == nil {
		state.indexTimers = make(map[string]*time.Timer)
	}
	if state.indexLatest == nil {
		state.indexLatest = make(map[string]indexRequest)
	}
	if state.indexSeq == nil {
		state.indexSeq = make(map[string]uint64)
	}

	state.indexSeq[uri]++
	seq := state.indexSeq[uri]
	state.indexLatest[uri] = indexRequest{seq: seq, uri: uri, path: path, content: content}

	if t, ok := state.indexTimers[uri]; ok && t != nil {
		t.Stop()
	}

	if debounce == 0 {
		state.indexMu.Unlock()
		state.Store.RemoveByFilePath(path)
		state.Indexer.IndexContent(path, content)
		return
	}

	state.indexTimers[uri] = time.AfterFunc(debounce, func() {
		state.indexMu.Lock()
		req, ok := state.indexLatest[uri]
		if !ok || req.seq != seq {
			state.indexMu.Unlock()
			return
		}
		p := req.path
		c := req.content
		state.indexMu.Unlock()

		state.Store.RemoveByFilePath(p)
		state.Indexer.IndexContent(p, c)
	})
	state.indexMu.Unlock()
}
