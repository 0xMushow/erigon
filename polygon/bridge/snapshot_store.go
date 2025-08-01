// Copyright 2024 The Erigon Authors
// This file is part of Erigon.
//
// Erigon is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// Erigon is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with Erigon. If not, see <http://www.gnu.org/licenses/>.

package bridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"time"

	"github.com/erigontech/erigon-lib/common"
	"github.com/erigontech/erigon-lib/common/length"
	"github.com/erigontech/erigon-lib/kv"
	"github.com/erigontech/erigon-lib/recsplit"
	"github.com/erigontech/erigon-lib/rlp"
	"github.com/erigontech/erigon-lib/snaptype"
	"github.com/erigontech/erigon/polygon/bor/types"
	"github.com/erigontech/erigon/polygon/heimdall"
	"github.com/erigontech/erigon/turbo/snapshotsync"
)

type SnapshotStore struct {
	Store
	snapshots              *heimdall.RoSnapshots
	sprintLengthCalculator sprintLengthCalculator
}

type sprintLengthCalculator interface {
	CalculateSprintLength(number uint64) uint64
}

func NewSnapshotStore(base Store, snapshots *heimdall.RoSnapshots, sprintLengthCalculator sprintLengthCalculator) *SnapshotStore {
	return &SnapshotStore{base, snapshots, sprintLengthCalculator}
}

func (s *SnapshotStore) Prepare(ctx context.Context) error {
	if err := s.Store.Prepare(ctx); err != nil {
		return err
	}

	return <-s.snapshots.Ready(ctx)
}

func (s *SnapshotStore) WithTx(tx kv.Tx) Store {
	return &SnapshotStore{txStore{tx: tx}, s.snapshots, s.sprintLengthCalculator}
}

func (s *SnapshotStore) RangeExtractor() snaptype.RangeExtractor {
	type extractableStore interface {
		RangeExtractor() snaptype.RangeExtractor
	}

	if extractableStore, ok := s.Store.(extractableStore); ok {
		return extractableStore.RangeExtractor()
	}
	return heimdall.Events.RangeExtractor()
}

func (s *SnapshotStore) LastFrozenEventBlockNum() uint64 {
	if s.snapshots == nil {
		return 0
	}

	tx := s.snapshots.ViewType(heimdall.Events)
	defer tx.Close()
	segments := tx.Segments

	if len(segments) == 0 {
		return 0
	}
	// find the last segment which has a built non-empty index
	var lastSegment *snapshotsync.VisibleSegment
	for i := len(segments) - 1; i >= 0; i-- {
		if segments[i].Src().Index() != nil {
			gg := segments[i].Src().MakeGetter()
			if gg.HasNext() {
				lastSegment = segments[i]
				break
			}
		}
	}
	if lastSegment == nil {
		return 0
	}
	var lastBlockNum uint64
	var buf []byte
	gg := lastSegment.Src().MakeGetter()
	for gg.HasNext() {
		buf, _ = gg.Next(buf[:0])
		lastBlockNum = binary.BigEndian.Uint64(buf[length.Hash : length.Hash+length.BlockNum])
	}

	return lastBlockNum
}

func (s *SnapshotStore) LastProcessedBlockInfo(ctx context.Context) (ProcessedBlockInfo, bool, error) {
	if blockInfo, ok, err := s.Store.LastProcessedBlockInfo(ctx); ok {
		return blockInfo, ok, err
	}

	tx := s.snapshots.ViewType(heimdall.Events)
	defer tx.Close()
	segments := tx.Segments

	if len(segments) == 0 {
		return ProcessedBlockInfo{}, false, nil
	}

	if s.sprintLengthCalculator == nil {
		return ProcessedBlockInfo{}, false, errors.New("can't calculate last block: missing sprint length calculator")
	}

	lastBlockNum := segments[len(segments)-1].To() - 1
	sprintLen := s.sprintLengthCalculator.CalculateSprintLength(lastBlockNum)
	lastBlockNum = (lastBlockNum / sprintLen) * sprintLen

	return ProcessedBlockInfo{
		BlockNum: lastBlockNum,
	}, true, nil
}

func (s *SnapshotStore) LastEventId(ctx context.Context) (uint64, error) {
	lastEventId, err := s.Store.LastEventId(ctx)

	if err != nil {
		return 0, err
	}

	snapshotLastEventId := s.LastFrozenEventId()

	return max(snapshotLastEventId, lastEventId), nil
}

func (s *SnapshotStore) LastFrozenEventId() uint64 {
	if s.snapshots == nil {
		return 0
	}

	tx := s.snapshots.ViewType(heimdall.Events)
	defer tx.Close()
	segments := tx.Segments

	if len(segments) == 0 {
		return 0
	}
	// find the last segment which has a built non-empty index
	var lastSegment *snapshotsync.VisibleSegment
	for i := len(segments) - 1; i >= 0; i-- {
		if segments[i].Src().Index() != nil {
			gg := segments[i].Src().MakeGetter()
			if gg.HasNext() {
				lastSegment = segments[i]
				break
			}
		}
	}
	if lastSegment == nil {
		return 0
	}
	var lastEventId uint64
	gg := lastSegment.Src().MakeGetter()
	var buf []byte
	for gg.HasNext() {
		buf, _ = gg.Next(buf[:0])
		lastEventId = binary.BigEndian.Uint64(buf[length.Hash+length.BlockNum : length.Hash+length.BlockNum+8])
	}
	return lastEventId
}

func (s *SnapshotStore) LastProcessedEventId(ctx context.Context) (uint64, error) {
	lastEventId, err := s.Store.LastProcessedEventId(ctx)

	if err != nil {
		return 0, err
	}

	snapshotLastEventId := s.LastFrozenEventId()

	return max(snapshotLastEventId, lastEventId), nil
}

func (s *SnapshotStore) EventTxnToBlockNum(ctx context.Context, txnHash common.Hash) (uint64, bool, error) {
	blockNum, ok, err := s.Store.EventTxnToBlockNum(ctx, txnHash)
	if err != nil {
		return 0, false, err
	}
	if ok {
		return blockNum, ok, nil
	}

	tx := s.snapshots.ViewType(heimdall.Events)
	defer tx.Close()
	segments := tx.Segments

	blockNum, ok, err = s.borBlockByEventHash(txnHash, segments, nil)
	if err != nil {
		return 0, false, err
	}
	if !ok {
		return 0, false, nil
	}
	return blockNum, true, nil
}

func (s *SnapshotStore) BlockEventIdsRange(ctx context.Context, blockHash common.Hash, blockNum uint64) (uint64, uint64, bool, error) {
	maxBlockNumInFiles := s.snapshots.VisibleBlocksAvailable(heimdall.Events.Enum())
	if maxBlockNumInFiles == 0 || blockNum > maxBlockNumInFiles {
		return s.Store.(interface {
			blockEventIdsRange(context.Context, common.Hash, uint64, uint64) (uint64, uint64, bool, error)
		}).blockEventIdsRange(ctx, blockHash, blockNum, s.LastFrozenEventId())
	}

	tx := s.snapshots.ViewType(heimdall.Events)
	defer tx.Close()
	segments := tx.Segments

	for i := len(segments) - 1; i >= 0; i-- {
		sn := segments[i]
		if sn.From() > blockNum {
			continue
		}
		if sn.To() <= blockNum {
			break
		}

		idxBorTxnHash := sn.Src().Index()
		if idxBorTxnHash == nil || idxBorTxnHash.KeyCount() == 0 {
			continue
		}

		reader := recsplit.NewIndexReader(idxBorTxnHash)
		txnHash := types.ComputeBorTxHash(blockNum, blockHash)
		blockEventId, exists := reader.Lookup(txnHash[:])
		var offset uint64

		gg := sn.Src().MakeGetter()
		if exists {
			offset = idxBorTxnHash.OrdinalLookup(blockEventId)
			gg.Reset(offset)
			if !gg.MatchPrefix(txnHash[:]) {
				continue
			}
		}

		var buf []byte
		for gg.HasNext() {
			buf, _ = gg.Next(buf[:0])
			if blockNum == binary.BigEndian.Uint64(buf[length.Hash:length.Hash+length.BlockNum]) {
				start := binary.BigEndian.Uint64(buf[length.Hash+length.BlockNum : length.Hash+length.BlockNum+8])
				end := start
				for gg.HasNext() {
					buf, _ = gg.Next(buf[:0])
					if blockNum != binary.BigEndian.Uint64(buf[length.Hash:length.Hash+length.BlockNum]) {
						break
					}
					end = binary.BigEndian.Uint64(buf[length.Hash+length.BlockNum : length.Hash+length.BlockNum+8])
				}
				return start, end, true, nil
			}
		}
	}

	return 0, 0, false, nil
}

func (s *SnapshotStore) events(ctx context.Context, start, end, blockNumber uint64) ([][]byte, error) {
	tx := s.snapshots.ViewType(heimdall.Events)
	defer tx.Close()
	segments := tx.Segments

	var buf []byte
	var result [][]byte

	for i := len(segments) - 1; i >= 0; i-- {
		if segments[i].From() > blockNumber {
			continue
		}
		if segments[i].To() <= blockNumber {
			break
		}

		gg0 := segments[i].Src().MakeGetter()

		if !gg0.HasNext() {
			continue
		}

		buf0, _ := gg0.Next(nil)
		if end <= binary.BigEndian.Uint64(buf0[length.Hash+length.BlockNum:length.Hash+length.BlockNum+8]) {
			continue
		}

		gg0.Reset(0)
		for gg0.HasNext() {
			buf, _ = gg0.Next(buf[:0])

			eventId := binary.BigEndian.Uint64(buf[length.Hash+length.BlockNum : length.Hash+length.BlockNum+8])

			if eventId < start {
				continue
			}

			if eventId >= end {
				return result, nil
			}

			result = append(result, bytes.Clone(buf[length.Hash+length.BlockNum+8:]))
		}
	}

	return result, nil
}

func (s *SnapshotStore) borBlockByEventHash(txnHash common.Hash, segments []*snapshotsync.VisibleSegment, buf []byte) (blockNum uint64, ok bool, err error) {
	for i := len(segments) - 1; i >= 0; i-- {
		sn := segments[i]
		idxBorTxnHash := sn.Src().Index()

		if idxBorTxnHash == nil {
			continue
		}
		if idxBorTxnHash.KeyCount() == 0 {
			continue
		}
		reader := recsplit.NewIndexReader(idxBorTxnHash)
		blockEventId, exists := reader.Lookup(txnHash[:])
		if !exists {
			continue
		}
		offset := idxBorTxnHash.OrdinalLookup(blockEventId)
		gg := sn.Src().MakeGetter()
		gg.Reset(offset)
		if !gg.MatchPrefix(txnHash[:]) {
			continue
		}
		buf, _ = gg.Next(buf[:0])
		blockNum = binary.BigEndian.Uint64(buf[length.Hash:])
		ok = true
		return
	}
	return
}

func (s *SnapshotStore) BorStartEventId(ctx context.Context, hash common.Hash, blockHeight uint64) (uint64, error) {
	startEventId, _, ok, err := s.BlockEventIdsRange(ctx, hash, blockHeight)
	if !ok || err != nil {
		return 0, err
	}
	return startEventId, nil
}

func (s *SnapshotStore) EventsByBlock(ctx context.Context, hash common.Hash, blockHeight uint64) ([]rlp.RawValue, error) {
	startEventId, endEventId, ok, err := s.BlockEventIdsRange(ctx, hash, blockHeight)
	if err != nil {
		return nil, err
	}
	if !ok {
		return []rlp.RawValue{}, nil
	}

	lastFrozenEventId := s.LastFrozenEventId()
	if startEventId > lastFrozenEventId || lastFrozenEventId == 0 {
		return s.Store.EventsByBlock(ctx, hash, blockHeight)
	}

	bytevals, err := s.events(ctx, startEventId, endEventId+1, blockHeight)
	if err != nil {
		return nil, err
	}
	result := make([]rlp.RawValue, len(bytevals))
	for i, byteval := range bytevals {
		result[i] = byteval
	}
	return result, nil
}

// EventsByIdFromSnapshot returns the list of records limited by time, or the number of records along with a bool value to signify if the records were limited by time
func (s *SnapshotStore) EventsByIdFromSnapshot(from uint64, to time.Time, limit int) ([]*heimdall.EventRecordWithTime, bool, error) {
	tx := s.snapshots.ViewType(heimdall.Events)
	defer tx.Close()
	segments := tx.Segments

	var buf []byte
	var result []*heimdall.EventRecordWithTime
	maxTime := false

	for _, sn := range segments {
		idxBorTxnHash := sn.Src().Index()

		if idxBorTxnHash == nil || idxBorTxnHash.KeyCount() == 0 {
			continue
		}

		offset := idxBorTxnHash.OrdinalLookup(0)
		gg := sn.Src().MakeGetter()
		gg.Reset(offset)
		for gg.HasNext() {
			buf, _ = gg.Next(buf[:0])

			raw := rlp.RawValue(common.Copy(buf[length.Hash+length.BlockNum+8:]))
			var event heimdall.EventRecordWithTime
			if err := event.UnmarshallBytes(raw); err != nil {
				return nil, false, err
			}

			if event.ID < from {
				continue
			}
			if event.Time.After(to) {
				maxTime = true
				return result, maxTime, nil
			}

			result = append(result, &event)

			if len(result) == limit {
				return result, maxTime, nil
			}
		}
	}

	return result, maxTime, nil
}
