// Copyright (c) 2015 - The Event Horizon authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package mongodb

import (
	"context"
	"errors"
	"fmt"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	mongoOptions "go.mongodb.org/mongo-driver/mongo/options"
	"go.mongodb.org/mongo-driver/mongo/readconcern"
	"go.mongodb.org/mongo-driver/mongo/readpref"
	"go.mongodb.org/mongo-driver/mongo/writeconcern"

	// Register uuid.UUID as BSON type.
	_ "github.com/looplab/eventhorizon/codec/bson"

	eh "github.com/looplab/eventhorizon"
	"github.com/looplab/eventhorizon/uuid"
)

var (
	// ErrCouldNotDialDB is when the database could not be dialed.
	ErrCouldNotDialDB = errors.New("could not dial database")
	// ErrNoDBClient is when no database client is set.
	ErrNoDBClient = errors.New("no database client")
	// ErrCouldNotClearDB is when the database could not be cleared.
	ErrCouldNotClearDB = errors.New("could not clear database")
	// ErrModelNotSet is when an model factory is not set on the Repo.
	ErrModelNotSet = errors.New("model not set")
	// ErrInvalidQuery is when a query was not returned from the callback to FindCustom.
	ErrInvalidQuery = errors.New("invalid query")
)

// Repo implements an MongoDB repository for entities.
type Repo struct {
	client    *mongo.Client
	entities  *mongo.Collection
	newEntity func() eh.Entity
}

// NewRepo creates a new Repo.
func NewRepo(uri, db, collection string, options ...Option) (*Repo, error) {
	opts := mongoOptions.Client().ApplyURI(uri)
	opts.SetWriteConcern(writeconcern.New(writeconcern.WMajority()))
	opts.SetReadConcern(readconcern.Majority())
	opts.SetReadPreference(readpref.Primary())
	client, err := mongo.Connect(context.TODO(), opts)
	if err != nil {
		return nil, ErrCouldNotDialDB
	}

	return NewRepoWithClient(client, db, collection, options...)
}

// NewRepoWithClient creates a new Repo with a client.
func NewRepoWithClient(client *mongo.Client, db, collection string, options ...Option) (*Repo, error) {
	if client == nil {
		return nil, ErrNoDBClient
	}

	r := &Repo{
		client:   client,
		entities: client.Database(db).Collection(collection),
	}

	for _, option := range options {
		if err := option(r); err != nil {
			return nil, fmt.Errorf("error while applying option: %w", err)
		}
	}

	return r, nil
}

// Option is an option setter used to configure creation.
type Option func(*Repo) error

// InnerRepo implements the InnerRepo method of the eventhorizon.ReadRepo interface.
func (r *Repo) InnerRepo(ctx context.Context) eh.ReadRepo {
	return nil
}

// IntoRepo tries to convert a eh.ReadRepo into a Repo by recursively looking at
// inner repos. Returns nil if none was found.
func IntoRepo(ctx context.Context, repo eh.ReadRepo) *Repo {
	if repo == nil {
		return nil
	}
	if r, ok := repo.(*Repo); ok {
		return r
	}
	return IntoRepo(ctx, repo.InnerRepo(ctx))
}

// Find implements the Find method of the eventhorizon.ReadRepo interface.
func (r *Repo) Find(ctx context.Context, id uuid.UUID) (eh.Entity, error) {
	if r.newEntity == nil {
		return nil, eh.RepoError{
			Err: ErrModelNotSet,
		}
	}

	entity := r.newEntity()
	if err := r.entities.FindOne(ctx, bson.M{"_id": id.String()}).Decode(entity); err == mongo.ErrNoDocuments {
		return nil, eh.RepoError{
			Err:     eh.ErrEntityNotFound,
			BaseErr: err,
		}
	} else if err != nil {
		return nil, eh.RepoError{
			Err:     eh.ErrCouldNotLoadEntity,
			BaseErr: err,
		}
	}

	return entity, nil
}

// FindAll implements the FindAll method of the eventhorizon.ReadRepo interface.
func (r *Repo) FindAll(ctx context.Context) ([]eh.Entity, error) {
	if r.newEntity == nil {
		return nil, eh.RepoError{
			Err: ErrModelNotSet,
		}
	}

	cursor, err := r.entities.Find(ctx, bson.M{})
	if err != nil {
		return nil, eh.RepoError{
			Err:     eh.ErrCouldNotLoadEntity,
			BaseErr: err,
		}
	}

	result := []eh.Entity{}
	for cursor.Next(ctx) {
		entity := r.newEntity()
		if err := cursor.Decode(entity); err != nil {
			return nil, eh.RepoError{
				Err:     eh.ErrCouldNotLoadEntity,
				BaseErr: err,
			}
		}
		result = append(result, entity)
	}

	if err := cursor.Close(ctx); err != nil {
		return nil, eh.RepoError{
			Err:     eh.ErrCouldNotLoadEntity,
			BaseErr: err,
		}
	}

	return result, nil
}

// The iterator is not thread safe.
type iter struct {
	cursor    *mongo.Cursor
	data      eh.Entity
	newEntity func() eh.Entity
	decodeErr error
}

func (i *iter) Next(ctx context.Context) bool {
	if !i.cursor.Next(ctx) {
		return false
	}

	item := i.newEntity()
	i.decodeErr = i.cursor.Decode(item)
	i.data = item
	return true
}

func (i *iter) Value() interface{} {
	return i.data
}

func (i *iter) Close(ctx context.Context) error {
	if err := i.cursor.Close(ctx); err != nil {
		return err
	}
	return i.decodeErr
}

// FindCustomIter returns a mgo cursor you can use to stream results of very large datasets
func (r *Repo) FindCustomIter(ctx context.Context, f func(context.Context, *mongo.Collection) (*mongo.Cursor, error)) (eh.Iter, error) {
	if r.newEntity == nil {
		return nil, eh.RepoError{
			Err: ErrModelNotSet,
		}
	}

	cursor, err := f(ctx, r.entities)
	if err != nil {
		return nil, eh.RepoError{
			Err:     ErrInvalidQuery,
			BaseErr: err,
		}
	}
	if cursor == nil {
		return nil, eh.RepoError{
			Err: ErrInvalidQuery,
		}
	}

	return &iter{
		cursor:    cursor,
		newEntity: r.newEntity,
	}, nil
}

// FindCustom uses a callback to specify a custom query for returning models.
// It can also be used to do queries that does not map to the model by executing
// the query in the callback and returning nil to block a second execution of
// the same query in FindCustom. Expect a ErrInvalidQuery if returning a nil
// query from the callback.
func (r *Repo) FindCustom(ctx context.Context, f func(context.Context, *mongo.Collection) (*mongo.Cursor, error)) ([]interface{}, error) {
	if r.newEntity == nil {
		return nil, eh.RepoError{
			Err: ErrModelNotSet,
		}
	}

	cursor, err := f(ctx, r.entities)
	if err != nil {
		return nil, eh.RepoError{
			Err:     ErrInvalidQuery,
			BaseErr: err,
		}
	}
	if cursor == nil {
		return nil, eh.RepoError{
			Err: ErrInvalidQuery,
		}
	}

	result := []interface{}{}
	entity := r.newEntity()
	for cursor.Next(ctx) {
		if err := cursor.Decode(entity); err != nil {
			return nil, eh.RepoError{
				Err:     eh.ErrCouldNotLoadEntity,
				BaseErr: err,
			}
		}
		result = append(result, entity)
		entity = r.newEntity()
	}
	if err := cursor.Close(ctx); err != nil {
		return nil, eh.RepoError{
			Err:     eh.ErrCouldNotLoadEntity,
			BaseErr: err,
		}
	}

	return result, nil
}

// Save implements the Save method of the eventhorizon.WriteRepo interface.
func (r *Repo) Save(ctx context.Context, entity eh.Entity) error {
	if entity.EntityID() == uuid.Nil {
		return eh.RepoError{
			Err:     eh.ErrCouldNotSaveEntity,
			BaseErr: eh.ErrMissingEntityID,
		}
	}

	if _, err := r.entities.UpdateOne(ctx,
		bson.M{
			"_id": entity.EntityID().String(),
		},
		bson.M{
			"$set": entity,
		},
		options.Update().SetUpsert(true),
	); err != nil {
		return eh.RepoError{
			Err:     eh.ErrCouldNotSaveEntity,
			BaseErr: err,
		}
	}
	return nil
}

// Remove implements the Remove method of the eventhorizon.WriteRepo interface.
func (r *Repo) Remove(ctx context.Context, id uuid.UUID) error {
	if r, err := r.entities.DeleteOne(ctx, bson.M{"_id": id.String()}); err != nil {
		return eh.RepoError{
			Err:     eh.ErrCouldNotRemoveEntity,
			BaseErr: err,
		}
	} else if r.DeletedCount == 0 {
		return eh.RepoError{
			Err: eh.ErrEntityNotFound,
		}
	}

	return nil
}

// Collection lets the function do custom actions on the collection.
func (r *Repo) Collection(ctx context.Context, f func(context.Context, *mongo.Collection) error) error {
	if err := f(ctx, r.entities); err != nil {
		return eh.RepoError{
			Err: err,
		}
	}

	return nil
}

// SetEntityFactory sets a factory function that creates concrete entity types.
func (r *Repo) SetEntityFactory(f func() eh.Entity) {
	r.newEntity = f
}

// Clear clears the read model database.
func (r *Repo) Clear(ctx context.Context) error {
	if err := r.entities.Drop(ctx); err != nil {
		return eh.RepoError{
			Err:     ErrCouldNotClearDB,
			BaseErr: err,
		}
	}
	return nil
}

// Close closes a database session.
func (r *Repo) Close(ctx context.Context) {
	r.client.Disconnect(ctx)
}
