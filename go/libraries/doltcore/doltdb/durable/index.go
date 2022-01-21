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

package durable

import (
	"context"
	"fmt"

	"github.com/dolthub/dolt/go/libraries/doltcore/schema"
	"github.com/dolthub/dolt/go/store/hash"
	"github.com/dolthub/dolt/go/store/prolly"
	"github.com/dolthub/dolt/go/store/types"
)

// Index represents a Table index.
type Index interface {
	// HashOf returns the hash.Hash of this table.
	HashOf() (hash.Hash, error)

	// Count returns the cardinality of the index.
	Count() uint64

	// Format returns the types.NomsBinFormat for this index.
	Format() *types.NomsBinFormat
}

// IndexSet stores a collection secondary Indexes.
type IndexSet interface {
	// HashOf returns the hash.Hash of this table.
	HashOf() (hash.Hash, error)

	// GetIndex gets an index from the set.
	GetIndex(ctx context.Context, sch schema.Schema, name string) (Index, error)

	// PutIndex puts an index into the set.
	PutIndex(ctx context.Context, name string, idx Index) (IndexSet, error)

	// PutNomsIndex puts a noms index into the set.
	// todo(andy): this is a temporary stop-gap while abstracting types.Map
	PutNomsIndex(ctx context.Context, name string, idx types.Map) (IndexSet, error)

	// DropIndex removes an index from the set.
	DropIndex(ctx context.Context, name string) (IndexSet, error)
}

func RefFromIndex(ctx context.Context, vrw types.ValueReadWriter, idx Index) (types.Ref, error) {
	switch idx.Format() {
	case types.Format_LD_1:
		return refFromNomsValue(ctx, vrw, idx.(nomsIndex).index)

	case types.Format_DOLT_1:
		b := prolly.ValueFromMap(idx.(prollyIndex).index)
		return refFromNomsValue(ctx, vrw, b)

	default:
		return types.Ref{}, errNbfUnkown
	}
}

func IndexFromRef(ctx context.Context, vrw types.ValueReadWriter, sch schema.Schema, r types.Ref) (Index, error) {
	v, err := r.TargetValue(ctx, vrw)
	if err != nil {
		return nil, err
	}

	switch vrw.Format() {
	case types.Format_LD_1:
		return IndexFromNomsMap(v.(types.Map), vrw), nil

	case types.Format_DOLT_1:
		pm := prolly.MapFromValue(v, sch, vrw)
		return IndexFromProllyMap(pm), nil

	default:
		return nil, errNbfUnkown
	}
}

func NewEmptyIndex(ctx context.Context, vrw types.ValueReadWriter, sch schema.Schema) (Index, error) {
	switch vrw.Format() {
	case types.Format_LD_1:
		m, err := types.NewMap(ctx, vrw)
		if err != nil {
			return nil, err
		}
		return IndexFromNomsMap(m, vrw), nil

	case types.Format_DOLT_1:
		kd, vd := prolly.MapDescriptorsFromScheam(sch)
		ns := prolly.NewNodeStore(prolly.ChunkStoreFromVRW(vrw))
		m, err := prolly.NewMapFromTuples(ctx, ns, kd, vd)
		if err != nil {
			return nil, err
		}
		return IndexFromProllyMap(m), nil

	default:
		return nil, errNbfUnkown
	}
}

type nomsIndex struct {
	index types.Map
	vrw   types.ValueReadWriter
}

func NomsMapFromIndex(i Index) (types.Map, error) {
	n, ok := i.(nomsIndex)
	if !ok {
		return types.Map{}, fmt.Errorf("unable to unwrap types.Map from Index")
	}
	return n.index, nil
}

func IndexFromNomsMap(m types.Map, vrw types.ValueReadWriter) Index {
	return nomsIndex{
		index: m,
		vrw:   vrw,
	}
}

var _ Index = nomsIndex{}

func (i nomsIndex) HashOf() (hash.Hash, error) {
	return i.index.Hash(i.vrw.Format())
}

func (i nomsIndex) Count() uint64 {
	return i.index.Len()
}

func (i nomsIndex) Format() *types.NomsBinFormat {
	return i.vrw.Format()
}

type prollyIndex struct {
	index prolly.Map
}

func ProllyMapFromIndex(i Index) prolly.Map {
	return i.(prollyIndex).index
}

func IndexFromProllyMap(m prolly.Map) Index {
	return prollyIndex{index: m}
}

var _ Index = prollyIndex{}

func (i prollyIndex) HashOf() (hash.Hash, error) {
	return i.index.HashOf(), nil
}

func (i prollyIndex) Count() uint64 {
	return i.index.Count()
}

func (i prollyIndex) Format() *types.NomsBinFormat {
	return i.index.Format()
}

func NewIndexSet(ctx context.Context, vrw types.ValueReadWriter) IndexSet {
	empty, _ := types.NewMap(ctx, vrw)
	return nomsIndexSet{
		indexes: empty,
		vrw:     vrw,
	}
}

type nomsIndexSet struct {
	indexes types.Map
	vrw     types.ValueReadWriter
}

var _ IndexSet = nomsIndexSet{}

// HashOf implements IndexSet.
func (s nomsIndexSet) HashOf() (hash.Hash, error) {
	return s.indexes.Hash(s.vrw.Format())
}

// GetIndex implements IndexSet.
func (s nomsIndexSet) GetIndex(ctx context.Context, sch schema.Schema, name string) (Index, error) {
	v, ok, err := s.indexes.MaybeGet(ctx, types.String(name))
	if !ok {
		err = fmt.Errorf("index %s not found in IndexSet", name)
	}
	if err != nil {
		return nil, err
	}

	idxSch := sch.Indexes().GetByName(name).Schema()

	return IndexFromRef(ctx, s.vrw, idxSch, v.(types.Ref))
}

// PutIndex implements IndexSet.
func (s nomsIndexSet) PutNomsIndex(ctx context.Context, name string, idx types.Map) (IndexSet, error) {
	return s.PutIndex(ctx, name, IndexFromNomsMap(idx, s.vrw))
}

// PutIndex implements IndexSet.
func (s nomsIndexSet) PutIndex(ctx context.Context, name string, idx Index) (IndexSet, error) {
	ref, err := RefFromIndex(ctx, s.vrw, idx)
	if err != nil {
		return nil, err
	}

	im, err := s.indexes.Edit().Set(types.String(name), ref).Map(ctx)
	if err != nil {
		return nil, err
	}

	return nomsIndexSet{indexes: im, vrw: s.vrw}, nil
}

// DropIndex implements IndexSet.
func (s nomsIndexSet) DropIndex(ctx context.Context, name string) (IndexSet, error) {
	im, err := s.indexes.Edit().Remove(types.String(name)).Map(ctx)
	if err != nil {
		return nil, err
	}

	return nomsIndexSet{indexes: im, vrw: s.vrw}, nil
}

func mapFromIndexSet(ic IndexSet) types.Map {
	return ic.(nomsIndexSet).indexes
}
