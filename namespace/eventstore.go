// Copyright (c) 2021 - The Event Horizon authors.
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

package namespace

import (
	"context"
	"fmt"
	"sync"

	eh "github.com/looplab/eventhorizon"
	"github.com/looplab/eventhorizon/uuid"
)

// EventStore is an event store with support for namespaces passed in the context.
type EventStore struct {
	eventStores   map[string]eh.EventStore
	eventStoresMu sync.RWMutex
	newEventStore func(ns string) (eh.EventStore, error)
}

// NewEventStore creates a new event store which will use the provided factory
// function to create new event stores for the provided namespace.
//
// Usage:
//    eventStore := NewEventStore(func(ns string) (eh.EventStore, error) {
//        s, err := mongodb.NewEventStore("mongodb://", ns)
//        if err != nil {
//            return nil, err
//        }
//        return s, nil
//    })
//
// Usage shared DB client:
//    client, err := mongo.Connect(ctx)
//    ...
//
//    eventStore := NewEventStore(func(ns string) (eh.EventStore, error) {
//        s, err := mongodb.NewEventStoreWithClient(client, ns)
//        if err != nil {
//            return nil, err
//        }
//        return s, nil
//    })
func NewEventStore(factory func(ns string) (eh.EventStore, error)) *EventStore {
	return &EventStore{
		eventStores:   map[string]eh.EventStore{},
		newEventStore: factory,
	}
}

// Save implements the Save method of the eventhorizon.EventStore interface.
func (s *EventStore) Save(ctx context.Context, events []eh.Event, originalVersion int) error {
	store, err := s.eventStore(ctx)
	if err != nil {
		return err
	}
	return store.Save(ctx, events, originalVersion)
}

// Load implements the Load method of the eventhorizon.EventStore interface.
func (s *EventStore) Load(ctx context.Context, id uuid.UUID) ([]eh.Event, error) {
	store, err := s.eventStore(ctx)
	if err != nil {
		return nil, err
	}
	return store.Load(ctx, id)
}

// eventStore is a helper that returns or creates event stores for each namespace.
func (s *EventStore) eventStore(ctx context.Context) (eh.EventStore, error) {
	ns := FromContext(ctx)
	s.eventStoresMu.RLock()
	eventStore, ok := s.eventStores[ns]
	s.eventStoresMu.RUnlock()
	if !ok {
		s.eventStoresMu.Lock()
		// Perform an additional existence check within the write lock in the
		// unlikely event that someone else added the event store right before us.
		if _, ok := s.eventStores[ns]; !ok {
			var err error
			eventStore, err = s.newEventStore(ns)
			if err != nil {
				return nil, fmt.Errorf("could not create event store for namespace '%s': %w", ns, err)
			}
			s.eventStores[ns] = eventStore
		}
		s.eventStoresMu.Unlock()
	}
	return eventStore, nil
}
