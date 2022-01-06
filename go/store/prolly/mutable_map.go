// Copyright 2021 Dolthub, Inc.
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

package prolly

import (
	"context"

	"github.com/dolthub/dolt/go/store/skip"

	"github.com/dolthub/dolt/go/store/val"
)

const (
	maxPending = 64 * 1024
)

type MutableMap struct {
	m       Map
	overlay memoryMap
}

func newMutableMap(m Map) MutableMap {
	return MutableMap{
		m:       m,
		overlay: newMemoryMap(m.keyDesc),
	}
}

// Map materializes the pending mutations in the MutableMap.
func (mut MutableMap) Map(ctx context.Context) (Map, error) {
	return materializeMutations(ctx, mut.m, mut.overlay.mutations())
}

// Put adds the Tuple pair |key|, |value| to the MutableMap.
func (mut MutableMap) Put(_ context.Context, key, value val.Tuple) error {
	mut.overlay.Put(key, value)
	return nil
}

// Get fetches the Tuple pair keyed by |key|, if it exists, and passes it to |cb|.
// If the |key| is not present in the MutableMap, a nil Tuple pair is passed to |cb|.
func (mut MutableMap) Get(ctx context.Context, key val.Tuple, cb KeyValueFn) (err error) {
	value, ok := mut.overlay.list.Get(key)
	if ok {
		if value == nil {
			// there is a pending delete of |key| in |mut.overlay|.
			key = nil
		}
		return cb(key, value)
	}

	return mut.m.Get(ctx, key, cb)
}

// Has returns true if |key| is present in the MutableMap.
func (mut MutableMap) Has(ctx context.Context, key val.Tuple) (ok bool, err error) {
	err = mut.Get(ctx, key, func(key, value val.Tuple) (err error) {
		ok = key != nil
		return
	})
	return
}

// IterAll returns a MapRangeIter that iterates over the entire MutableMap.
func (mut MutableMap) IterAll(ctx context.Context) (MapRangeIter, error) {
	rng := Range{
		Start:   RangeCut{Unbound: true},
		Stop:    RangeCut{Unbound: true},
		KeyDesc: mut.m.keyDesc,
		Reverse: false,
	}
	return mut.IterValueRange(ctx, rng)
}

// IterValueRange returns a MapRangeIter that iterates over a Range.
func (mut MutableMap) IterValueRange(ctx context.Context, rng Range) (MapRangeIter, error) {
	var iter *skip.ListIter
	if rng.Start.Unbound {
		if rng.Reverse {
			iter = mut.overlay.list.IterAtEnd()
		} else {
			iter = mut.overlay.list.IterAtStart()
		}
	} else {
		iter = mut.overlay.list.IterAt(rng.Start.Key)
	}
	memCur := memTupleCursor{iter: iter}

	var err error
	var cur *nodeCursor
	if rng.Start.Unbound {
		if rng.Reverse {
			cur, err = mut.m.cursorAtEnd(ctx)
		} else {
			cur, err = mut.m.cursorAtStart(ctx)
		}
	} else {
		cur, err = mut.m.cursorAtkey(ctx, rng.Start.Key)
	}
	if err != nil {
		return MapRangeIter{}, err
	}
	proCur := mapTupleCursor{cur: cur}

	return NewMapRangeIter(ctx, memCur, proCur, rng)
}
