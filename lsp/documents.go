package lsp

import "sync"

// DocumentStore tracks the content of open text documents.
type DocumentStore struct {
	mu   sync.RWMutex
	docs map[string]*Document
}

// Document represents an open text document.
type Document struct {
	URI     string
	Version int
	Content string
}

// NewDocumentStore creates a new document store.
func NewDocumentStore() *DocumentStore {
	return &DocumentStore{docs: make(map[string]*Document)}
}

// Open adds a document to the store.
func (ds *DocumentStore) Open(uri string, version int, content string) {
	ds.mu.Lock()
	defer ds.mu.Unlock()
	ds.docs[uri] = &Document{URI: uri, Version: version, Content: content}
}

// Update replaces the content of an open document.
func (ds *DocumentStore) Update(uri string, version int, content string) {
	ds.mu.Lock()
	defer ds.mu.Unlock()
	if doc, ok := ds.docs[uri]; ok {
		doc.Version = version
		doc.Content = content
	}
}

// Get returns a document by URI.
func (ds *DocumentStore) Get(uri string) (*Document, bool) {
	ds.mu.RLock()
	defer ds.mu.RUnlock()
	doc, ok := ds.docs[uri]
	return doc, ok
}

// Close removes a document from the store.
func (ds *DocumentStore) Close(uri string) {
	ds.mu.Lock()
	defer ds.mu.Unlock()
	delete(ds.docs, uri)
}
