package storage

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	pb "github.com/qdrant/go-client/qdrant"
)

// QdrantStore wraps the Qdrant gRPC client for vector storage operations.
type QdrantStore struct {
	client         *pb.Client
	collectionName string
}

// SearchResult holds a single search result from Qdrant.
type SearchResult struct {
	ID      string
	Score   float32
	Payload map[string]any
}

// NewQdrantStore connects to Qdrant at the given gRPC address (e.g. "localhost:6334")
// and returns a QdrantStore bound to the specified collection.
func NewQdrantStore(host string, collectionName string) (*QdrantStore, error) {
	h, p, err := parseQdrantAddr(host)
	if err != nil {
		return nil, fmt.Errorf("qdrant parse addr: %w", err)
	}

	client, err := pb.NewClient(&pb.Config{
		Host: h,
		Port: p,
	})
	if err != nil {
		return nil, fmt.Errorf("qdrant connect: %w", err)
	}

	return &QdrantStore{
		client:         client,
		collectionName: collectionName,
	}, nil
}

// Close closes the underlying gRPC connection.
func (q *QdrantStore) Close() error {
	return q.client.Close()
}

// EnsureCollection checks if the collection exists and creates it if not.
// The collection is configured with cosine distance, the given vector dimension,
// and int8 scalar quantization (quantile 0.99, always in RAM).
func (q *QdrantStore) EnsureCollection(ctx context.Context, dimension uint64) error {
	exists, err := q.client.CollectionExists(ctx, q.collectionName)
	if err != nil {
		return fmt.Errorf("qdrant check collection: %w", err)
	}
	if exists {
		return nil
	}

	err = q.client.CreateCollection(ctx, &pb.CreateCollection{
		CollectionName: q.collectionName,
		VectorsConfig: pb.NewVectorsConfig(&pb.VectorParams{
			Size:     dimension,
			Distance: pb.Distance_Cosine,
		}),
		QuantizationConfig: pb.NewQuantizationScalar(&pb.ScalarQuantization{
			Type:      pb.QuantizationType_Int8,
			Quantile:  pb.PtrOf(float32(0.99)),
			AlwaysRam: pb.PtrOf(true),
		}),
	})
	if err != nil {
		return fmt.Errorf("qdrant create collection: %w", err)
	}
	return nil
}

// UpsertVectors inserts or updates points with UUID-based IDs, vectors, and payloads.
// Each payload should contain "document_id" (string) and "chunk_index" (int).
func (q *QdrantStore) UpsertVectors(ctx context.Context, ids []string, vectors [][]float32, payloads []map[string]any) error {
	points := make([]*pb.PointStruct, len(ids))
	for i := range ids {
		points[i] = &pb.PointStruct{
			Id:      pb.NewIDUUID(ids[i]),
			Vectors: pb.NewVectorsDense(vectors[i]),
			Payload: pb.NewValueMap(payloads[i]),
		}
	}

	_, err := q.client.Upsert(ctx, &pb.UpsertPoints{
		CollectionName: q.collectionName,
		Points:         points,
		Wait:           pb.PtrOf(true),
	})
	if err != nil {
		return fmt.Errorf("qdrant upsert: %w", err)
	}
	return nil
}

// Search performs a vector similarity search, returning up to limit results
// with their IDs, scores, and payloads.
func (q *QdrantStore) Search(ctx context.Context, vector []float32, limit uint64) ([]SearchResult, error) {
	scored, err := q.client.Query(ctx, &pb.QueryPoints{
		CollectionName: q.collectionName,
		Query:          pb.NewQueryDense(vector),
		Limit:          pb.PtrOf(limit),
		WithPayload:    pb.NewWithPayloadEnable(true),
	})
	if err != nil {
		return nil, fmt.Errorf("qdrant search: %w", err)
	}

	results := make([]SearchResult, len(scored))
	for i, r := range scored {
		results[i] = SearchResult{
			ID:      r.Id.GetUuid(),
			Score:   r.Score,
			Payload: valueMapToGo(r.GetPayload()),
		}
	}
	return results, nil
}

// DeleteByDocumentID deletes all points where the payload field "document_id"
// matches the given documentID.
func (q *QdrantStore) DeleteByDocumentID(ctx context.Context, documentID string) error {
	_, err := q.client.Delete(ctx, &pb.DeletePoints{
		CollectionName: q.collectionName,
		Points: pb.NewPointsSelectorFilter(&pb.Filter{
			Must: []*pb.Condition{
				pb.NewMatch("document_id", documentID),
			},
		}),
		Wait: pb.PtrOf(true),
	})
	if err != nil {
		return fmt.Errorf("qdrant delete by document_id: %w", err)
	}
	return nil
}

// parseQdrantAddr splits "host:port" into host string and port int.
func parseQdrantAddr(addr string) (string, int, error) {
	parts := strings.SplitN(addr, ":", 2)
	if len(parts) != 2 {
		return "", 0, fmt.Errorf("invalid address %q: expected host:port", addr)
	}
	port, err := strconv.Atoi(parts[1])
	if err != nil {
		return "", 0, fmt.Errorf("invalid port in %q: %w", addr, err)
	}
	return parts[0], port, nil
}

// valueMapToGo converts a Qdrant payload map to a generic Go map.
func valueMapToGo(m map[string]*pb.Value) map[string]any {
	if m == nil {
		return nil
	}
	result := make(map[string]any, len(m))
	for k, v := range m {
		result[k] = valueToGo(v)
	}
	return result
}

// valueToGo converts a single Qdrant Value to a Go value.
func valueToGo(v *pb.Value) any {
	if v == nil {
		return nil
	}
	switch kind := v.GetKind().(type) {
	case *pb.Value_NullValue:
		return nil
	case *pb.Value_BoolValue:
		return kind.BoolValue
	case *pb.Value_IntegerValue:
		return kind.IntegerValue
	case *pb.Value_DoubleValue:
		return kind.DoubleValue
	case *pb.Value_StringValue:
		return kind.StringValue
	case *pb.Value_StructValue:
		fields := kind.StructValue.GetFields()
		m := make(map[string]any, len(fields))
		for k, fv := range fields {
			m[k] = valueToGo(fv)
		}
		return m
	case *pb.Value_ListValue:
		vals := kind.ListValue.GetValues()
		list := make([]any, len(vals))
		for i, lv := range vals {
			list[i] = valueToGo(lv)
		}
		return list
	default:
		return nil
	}
}
