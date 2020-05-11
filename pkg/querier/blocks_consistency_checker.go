package querier

import (
	"fmt"
	"strings"
	"time"

	"github.com/oklog/ulid"
	"github.com/thanos-io/thanos/pkg/block/metadata"
	"github.com/thanos-io/thanos/pkg/store/hintspb"
)

type BlocksConsistencyChecker struct {
	uploadGracePeriod  time.Duration
	deletionMarksDelay time.Duration
}

func NewBlocksConsistencyChecker(uploadGracePeriod, deletionMarksDelay time.Duration) *BlocksConsistencyChecker {
	return &BlocksConsistencyChecker{
		uploadGracePeriod:  uploadGracePeriod,
		deletionMarksDelay: deletionMarksDelay,
	}
}

func (c *BlocksConsistencyChecker) Check(knownBlocks []*metadata.Meta, knownDeletionMarks map[ulid.ULID]*metadata.DeletionMark, queried map[string][]hintspb.Block) error {
	// Reverse the map of queried blocks, so that we can easily look for missing ones
	// while keeping the information about which store-gateways have already been queried
	// for that block.
	actualBlocks := map[string][]string{}
	for gatewayAddr, blocks := range queried {
		for _, b := range blocks {
			actualBlocks[b.Id] = append(actualBlocks[b.Id], gatewayAddr)
		}
	}

	// Look for any missing block.
	missingBlocks := map[string][]string{}
	var missingBlockIDs []string

	for _, meta := range knownBlocks {
		// Some recently uploaded blocks, already discovered by the querier, may not have been discovered
		// and loaded by the store-gateway yet. In order to avoid false positives, we grant some time
		// to the store-gateway to discover them. It's safe to exclude recently uploaded blocks because:
		// - Blocks uploaded by ingesters: we will continue querying them from ingesters for a while (depends
		//   on the configured retention period).
		// - Blocks uploaded by compactor: the source blocks are marked for deletion but will continue to be
		//   queried by store-gateways for a while (depends on the configured deletion marks delay).
		if ulid.Now()-meta.ULID.Time() < uint64(c.uploadGracePeriod/time.Millisecond) {
			continue
		}

		// The store-gateway may offload blocks before the querier. If that happen, the querier will run a consistency check
		// on blocks that can't be queried because offloaded. For this reason, we don't run the consistency check on any block
		// which has been marked for deletion more then X time ago, where X is the deletion delay / 2.
		if mark := knownDeletionMarks[meta.ULID]; mark != nil {
			deletionGracePeriod := c.deletionMarksDelay / 2
			if time.Since(time.Unix(mark.DeletionTime, 0)).Seconds() > deletionGracePeriod.Seconds() {
				continue
			}
		}

		id := meta.ULID.String()
		if gatewayAddrs, ok := actualBlocks[id]; !ok {
			missingBlocks[id] = gatewayAddrs
			missingBlockIDs = append(missingBlockIDs, id)
		}
	}

	if len(missingBlocks) == 0 {
		return nil
	}

	return fmt.Errorf("consistency check failed because of non-queried blocks: %s", strings.Join(missingBlockIDs, " "))
}
