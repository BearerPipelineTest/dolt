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
	"bytes"
	"context"
	"io"

	"golang.org/x/sync/errgroup"

	"github.com/dolthub/dolt/go/store/val"
)

const patchBufferSize = 1024

type TupleMergeFn func(left, right Diff) (Diff, bool)

// ThreeWayMerge implements a three-way merge algorithm using |base| as the common ancestor, |right| as
// the source branch, and |left| as the destination branch. Both |left| and |right| are diff'd against
// |base| to compute merge patches, but rather than applying both sets of patches to |base|, patches from
// |right| are applied directly to |left|. This reduces the amount of write work and improves performance.
// In the case that a key-value pair was modified on both |left| and |right| with different resulting
// values, the TupleMergeFn is called to perform a cell-wise merge, or to throw a conflict.
func ThreeWayMerge(ctx context.Context, base, left, right Map, cb TupleMergeFn) (final Map, err error) {
	ld, err := treeDifferFromMaps(ctx, base, left)
	if err != nil {
		return Map{}, err
	}

	rd, err := treeDifferFromMaps(ctx, base, right)
	if err != nil {
		return Map{}, err
	}

	eg, ctx := errgroup.WithContext(ctx)
	buf := newPatchBuffer(patchBufferSize)

	eg.Go(func() error {
		defer buf.close()
		return sendPatches(ctx, ld, rd, buf, cb)
	})

	eg.Go(func() error {
		final, err = materializeMutations(ctx, left, buf)
		return err
	})

	if err = eg.Wait(); err != nil {
		return Map{}, err
	}

	return final, nil
}

// patchBuffer implements mutationIter. It consumes Diffs
// from the parallel treeDiffers and transforms them into
// patches for the treeChunker to apply.
type patchBuffer struct {
	buf chan patch
}

var _ mutationIter = patchBuffer{}

type patch [2]val.Tuple

func newPatchBuffer(sz int) patchBuffer {
	return patchBuffer{buf: make(chan patch, sz)}
}

func (ps patchBuffer) sendPatch(ctx context.Context, diff Diff) error {
	p := patch{diff.Key, diff.To}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case ps.buf <- p:
		return nil
	}
}

// nextMutation implements mutationIter.
func (ps patchBuffer) nextMutation(ctx context.Context) (val.Tuple, val.Tuple) {
	var p patch
	select {
	case p = <-ps.buf:
		return p[0], p[1]
	case <-ctx.Done():
		return nil, nil
	}
}

func (ps patchBuffer) close() {
	close(ps.buf)
}

func sendPatches(ctx context.Context, l, r treeDiffer, buf patchBuffer, cb TupleMergeFn) (err error) {
	var (
		left, right Diff
		lok, rok    = true, true
	)

	left, err = l.Next(ctx)
	if err == io.EOF {
		err, lok = nil, false
	}
	if err != nil {
		return err
	}

	right, err = r.Next(ctx)
	if err == io.EOF {
		err, rok = nil, false
	}
	if err != nil {
		return err
	}

	for lok && rok {
		cmp := compareDiffKeys(left, right, l.cmp)

		switch {
		case cmp < 0:
			// already in left
			left, err = l.Next(ctx)
			if err == io.EOF {
				err, lok = nil, false
			}
			if err != nil {
				return err
			}

		case cmp > 0:
			err = buf.sendPatch(ctx, right)
			if err != nil {
				return err
			}

			right, err = r.Next(ctx)
			if err == io.EOF {
				err, rok = nil, false
			}
			if err != nil {
				return err
			}

		case cmp == 0:
			if equalDiffVals(left, right) {
				// already in left
				continue
			}

			resolved, ok := cb(left, right)
			if ok {
				err = buf.sendPatch(ctx, resolved)
			}
			if err != nil {
				return err
			}
		}
	}

	for lok {
		// already in left
		break
	}

	for rok {
		err = buf.sendPatch(ctx, right)
		if err != nil {
			return err
		}

		right, err = r.Next(ctx)
		if err == io.EOF {
			err, rok = nil, false
		}
		if err != nil {
			return err
		}
	}

	return nil
}

func compareDiffKeys(left, right Diff, cmp compareFn) int {
	return cmp(nodeItem(left.Key), nodeItem(right.Key))
}

func equalDiffVals(left, right Diff) bool {
	// todo(andy): bytes must be comparable
	ok := left.Type == right.Type
	return ok && bytes.Equal(left.To, right.To)
}
