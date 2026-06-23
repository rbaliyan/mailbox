//go:build integration

// Package mongo integration tests run the shared store conformance, concurrency,
// and outbox suites against a live MongoDB instance.
//
// They are gated behind the "integration" build tag and require a MongoDB
// connection string in the MONGO_URI environment variable. The instance must be
// a replica set (or otherwise transaction-capable) for the outbox suite to
// exercise real transactions; a standalone server falls back to non-transactional
// execution.
//
//	MONGO_URI=mongodb://localhost:27019/?directConnection=true \
//	    go test -tags integration -race ./store/mongo/...
//
// docker-compose.test.yml provisions a single-node replica set on port 27019.
package mongo

import (
	"context"
	"fmt"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rbaliyan/mailbox/store"
	"github.com/rbaliyan/mailbox/store/storetest"
	"go.mongodb.org/mongo-driver/v2/mongo"
	mongoopts "go.mongodb.org/mongo-driver/v2/mongo/options"
)

const testDatabase = "mailbox_test"

var collectionSeq uint64

// uniqueCollection returns a process-unique collection name so each store
// obtained from the factory is fully isolated from every other.
func uniqueCollection(prefix string) string {
	n := atomic.AddUint64(&collectionSeq, 1)
	return fmt.Sprintf("%s_%d_%d", prefix, time.Now().UnixNano(), n)
}

// dialClient connects a MongoDB client from MONGO_URI, skipping the test when
// the variable is unset. The client is closed via t.Cleanup.
func dialClient(t *testing.T) *mongo.Client {
	t.Helper()
	uri := os.Getenv("MONGO_URI")
	if uri == "" {
		t.Skip("set MONGO_URI to run MongoDB integration tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client, err := mongo.Connect(mongoopts.Client().ApplyURI(uri))
	if err != nil {
		t.Fatalf("mongo connect: %v", err)
	}
	if err := client.Ping(ctx, nil); err != nil {
		t.Fatalf("mongo ping: %v", err)
	}
	t.Cleanup(func() { _ = client.Disconnect(context.Background()) })
	return client
}

// newStore builds a connected store on a fresh collection, dropping that
// collection on cleanup. extra options let callers enable the outbox.
func newStore(t *testing.T, client *mongo.Client, extra ...Option) store.Store {
	t.Helper()
	coll := uniqueCollection("messages")
	opts := append([]Option{
		WithDatabase(testDatabase),
		WithCollection(coll),
		WithOutboxCollection(coll + "_outbox"),
	}, extra...)
	s := New(client, opts...)
	if err := s.Connect(context.Background()); err != nil {
		t.Fatalf("store connect: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = client.Database(testDatabase).Collection(coll).Drop(ctx)
		_ = client.Database(testDatabase).Collection(coll + "_outbox").Drop(ctx)
		_ = s.Close(context.Background())
	})
	return s
}

// sharedStoreFactory returns a NewStoreFunc that hands every subtest the same
// connected store. The conformance and concurrency suites isolate subtests with
// unique owner IDs, so a single collection is sufficient — and sharing it avoids
// building the full secondary-index set on a throwaway collection per subtest,
// which otherwise floods the single-node test server with index builds.
func sharedStoreFactory(t *testing.T, client *mongo.Client, extra ...Option) storetest.NewStoreFunc {
	t.Helper()
	s := newStore(t, client, extra...)
	return func(t *testing.T) store.Store { return s }
}

func TestMongoConformance(t *testing.T) {
	client := dialClient(t)
	storetest.RunStoreSuite(t, sharedStoreFactory(t, client))
}

func TestMongoConcurrency(t *testing.T) {
	client := dialClient(t)
	storetest.RunConcurrencySuite(t, sharedStoreFactory(t, client))
}

func TestMongoOutbox(t *testing.T) {
	client := dialClient(t)
	storetest.RunOutboxSuite(t, sharedStoreFactory(t, client, WithOutbox(true)))
}
