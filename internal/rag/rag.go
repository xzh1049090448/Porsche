package rag

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const DatasetAttribution = "回答参考了平台专属数据集"

type document struct {
	DatasetID int    `json:"dataset_id"`
	Text      string `json:"text"`
}

type Engine struct {
	mu         sync.RWMutex
	persistDir string
	topK       int
	docs       []document
}

func NewEngine(persistDir string, topK int) *Engine {
	e := &Engine{
		persistDir: persistDir,
		topK:       topK,
	}
	_ = os.MkdirAll(persistDir, 0o755)
	e.load()
	return e
}

func (e *Engine) persistPath() string {
	return filepath.Join(e.persistDir, "documents.json")
}

func (e *Engine) load() {
	raw, err := os.ReadFile(e.persistPath())
	if err != nil {
		return
	}
	var docs []document
	if json.Unmarshal(raw, &docs) == nil {
		e.docs = docs
	}
}

func (e *Engine) saveLocked() {
	raw, _ := json.Marshal(e.docs)
	_ = os.WriteFile(e.persistPath(), raw, 0o644)
}

func (e *Engine) IndexDocuments(datasetID int, texts []string) {
	e.mu.Lock()
	defer e.mu.Unlock()

	filtered := e.docs[:0]
	for _, d := range e.docs {
		if d.DatasetID != datasetID {
			filtered = append(filtered, d)
		}
	}
	for _, t := range texts {
		filtered = append(filtered, document{DatasetID: datasetID, Text: t})
	}
	e.docs = filtered
	e.saveLocked()
}

func (e *Engine) Search(datasetIDs []int, query string) []string {
	e.mu.RLock()
	defer e.mu.RUnlock()

	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return nil
	}

	allowed := make(map[int]struct{}, len(datasetIDs))
	for _, id := range datasetIDs {
		allowed[id] = struct{}{}
	}

	type scored struct {
		text  string
		score int
	}
	var hits []scored
	for _, d := range e.docs {
		if _, ok := allowed[d.DatasetID]; !ok {
			continue
		}
		lower := strings.ToLower(d.Text)
		score := 0
		for _, term := range strings.Fields(query) {
			if len(term) < 2 {
				continue
			}
			if strings.Contains(lower, term) {
				score++
			}
		}
		if score > 0 {
			hits = append(hits, scored{text: d.Text, score: score})
		}
	}

	// simple selection sort for topK
	for i := 0; i < len(hits); i++ {
		for j := i + 1; j < len(hits); j++ {
			if hits[j].score > hits[i].score {
				hits[i], hits[j] = hits[j], hits[i]
			}
		}
	}
	limit := e.topK
	if limit > len(hits) {
		limit = len(hits)
	}
	out := make([]string, 0, limit)
	for i := 0; i < limit; i++ {
		out = append(out, hits[i].text)
	}
	return out
}

func (e *Engine) BuildRAGMessages(messages []map[string]interface{}, datasetIDs []int, query string) ([]map[string]interface{}, bool) {
	chunks := e.Search(datasetIDs, query)
	if len(chunks) == 0 {
		return messages, false
	}
	context := strings.Join(chunks, "\n\n")
	system := map[string]interface{}{
		"role":    "system",
		"content": "以下是与用户问题相关的参考资料，请基于资料回答：\n\n" + context,
	}
	out := append([]map[string]interface{}{system}, messages...)
	return out, true
}
