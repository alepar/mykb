package storage

import (
	"context"
	"errors"
	"fmt"
	"time"

	meilisearch "github.com/meilisearch/meilisearch-go"
)

const defaultMeiliWaitInterval = 250 * time.Millisecond

// MeilisearchStore provides full-text search storage backed by Meilisearch.
type MeilisearchStore struct {
	client    meilisearch.ServiceManager
	indexName string
}

// MeiliChunk is the document schema stored in the Meilisearch index.
type MeiliChunk struct {
	ChunkID    string `json:"chunk_id"`
	DocumentID string `json:"document_id"`
	ChunkIndex int    `json:"chunk_index"`
	Content    string `json:"content"`
}

// MeiliHit represents a single search result from Meilisearch.
type MeiliHit struct {
	ChunkID      string
	DocumentID   string
	ChunkIndex   int
	RankingScore float64
}

// NewMeilisearchStore creates a new MeilisearchStore connected to the given host.
func NewMeilisearchStore(host, apiKey, indexName string) (*MeilisearchStore, error) {
	if host == "" {
		return nil, errors.New("meilisearch host cannot be empty")
	}
	if indexName == "" {
		return nil, errors.New("meilisearch index name cannot be empty")
	}
	client := meilisearch.New(host, meilisearch.WithAPIKey(apiKey))
	return &MeilisearchStore{
		client:    client,
		indexName: indexName,
	}, nil
}

// EnsureIndex creates the index if it does not exist and configures its settings.
func (m *MeilisearchStore) EnsureIndex(ctx context.Context) error {
	// Check if index already exists.
	_, err := m.client.GetIndex(m.indexName)
	if err == nil {
		// Index exists; update settings to ensure they are current.
		return m.updateSettings(ctx)
	}

	var apiErr *meilisearch.Error
	if errors.As(err, &apiErr) && apiErr.MeilisearchApiError.Code == "index_not_found" {
		// Create the index.
		task, createErr := m.client.CreateIndex(&meilisearch.IndexConfig{
			Uid:        m.indexName,
			PrimaryKey: "chunk_id",
		})
		if createErr != nil {
			return fmt.Errorf("create index %q: %w", m.indexName, createErr)
		}
		if err := m.waitForTask(ctx, task); err != nil {
			return fmt.Errorf("wait for index creation: %w", err)
		}
		return m.updateSettings(ctx)
	}

	return fmt.Errorf("get index %q: %w", m.indexName, err)
}

func (m *MeilisearchStore) updateSettings(ctx context.Context) error {
	index := m.client.Index(m.indexName)
	task, err := index.UpdateSettings(&meilisearch.Settings{
		SearchableAttributes: []string{"content"},
		FilterableAttributes: []string{"chunk_id", "document_id", "chunk_index"},
		DisplayedAttributes:  []string{"chunk_id", "document_id", "chunk_index"},
	})
	if err != nil {
		return fmt.Errorf("update settings for index %q: %w", m.indexName, err)
	}
	return m.waitForTask(ctx, task)
}

// IndexChunks adds or replaces chunks in the index and waits for completion.
func (m *MeilisearchStore) IndexChunks(ctx context.Context, chunks []MeiliChunk) error {
	if len(chunks) == 0 {
		return nil
	}
	index := m.client.Index(m.indexName)
	task, err := index.AddDocuments(chunks, nil)
	if err != nil {
		return fmt.Errorf("add documents to index %q: %w", m.indexName, err)
	}
	return m.waitForTask(ctx, task)
}

// Search performs a full-text search and returns parsed hits with ranking scores.
func (m *MeilisearchStore) Search(ctx context.Context, query string, limit int64) ([]MeiliHit, error) {
	index := m.client.Index(m.indexName)
	resp, err := index.Search(query, &meilisearch.SearchRequest{
		Limit:            limit,
		ShowRankingScore: true,
	})
	if err != nil {
		return nil, fmt.Errorf("search index %q: %w", m.indexName, err)
	}

	hits := make([]MeiliHit, 0, len(resp.Hits))
	for i, hit := range resp.Hits {
		var raw meiliHitRaw
		if err := hit.DecodeInto(&raw); err != nil {
			return nil, fmt.Errorf("decode hit %d: %w", i, err)
		}
		hits = append(hits, MeiliHit{
			ChunkID:      raw.ChunkID,
			DocumentID:   raw.DocumentID,
			ChunkIndex:   raw.ChunkIndex,
			RankingScore: raw.RankingScore,
		})
	}
	return hits, nil
}

// DeleteByDocumentID removes all chunks belonging to the given document ID.
func (m *MeilisearchStore) DeleteByDocumentID(ctx context.Context, documentID string) error {
	index := m.client.Index(m.indexName)
	task, err := index.DeleteDocumentsByFilter(
		fmt.Sprintf("document_id = '%s'", documentID), nil,
	)
	if err != nil {
		return fmt.Errorf("delete documents for document_id %q: %w", documentID, err)
	}
	return m.waitForTask(ctx, task)
}

// waitForTask polls Meilisearch until the task completes or the context is cancelled.
func (m *MeilisearchStore) waitForTask(ctx context.Context, task *meilisearch.TaskInfo) error {
	if task == nil {
		return errors.New("task info cannot be nil")
	}
	for {
		current, err := m.client.GetTask(task.TaskUID)
		if err != nil {
			return fmt.Errorf("get task %d: %w", task.TaskUID, err)
		}
		switch current.Status {
		case meilisearch.TaskStatusSucceeded:
			return nil
		case meilisearch.TaskStatusFailed, meilisearch.TaskStatusCanceled, meilisearch.TaskStatusUnknown:
			if current.Error.Message != "" {
				return fmt.Errorf("task %d ended with status %s: %s", current.TaskUID, current.Status, current.Error.Message)
			}
			return fmt.Errorf("task %d ended with status %s", current.TaskUID, current.Status)
		default:
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(defaultMeiliWaitInterval):
				continue
			}
		}
	}
}

// meiliHitRaw is the JSON-tagged struct used for decoding Meilisearch hits.
type meiliHitRaw struct {
	ChunkID      string  `json:"chunk_id"`
	DocumentID   string  `json:"document_id"`
	ChunkIndex   int     `json:"chunk_index"`
	RankingScore float64 `json:"_rankingScore"`
}
