// Copyright (C) 2019 Storj Labs, Inc.
// See LICENSE for copying information.

package metrics_test

import (
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"

	"storj.io/common/memory"
	"storj.io/common/testcontext"
	"storj.io/common/testrand"
	"storj.io/storj/private/testplanet"
)

func TestCounterInlineAndRemote(t *testing.T) {
	testplanet.Run(t, testplanet.Config{
		SatelliteCount: 1, StorageNodeCount: 4, UplinkCount: 1,
	}, func(t *testing.T, ctx *testcontext.Context, planet *testplanet.Planet) {
		satellite := planet.Satellites[0]
		ul := planet.Uplinks[0]
		metricsChore := satellite.Metrics.Chore
		metricsChore.Loop.Pause()

		segmentSize := 8 * memory.KiB

		// upload 2 inline files
		for i := 0; i < 2; i++ {
			testData := testrand.Bytes(segmentSize / 8)
			path := "/some/inline/path/" + strconv.Itoa(i)
			err := ul.Upload(ctx, satellite, "bucket", path, testData)
			require.NoError(t, err)
		}

		// upload 2 remote files with 1 segment
		for i := 0; i < 2; i++ {
			testData := testrand.Bytes(segmentSize)
			path := "/some/remote/path/" + strconv.Itoa(i)
			err := ul.Upload(ctx, satellite, "testbucket", path, testData)
			require.NoError(t, err)
		}

		metricsChore.Loop.TriggerWait()
		require.EqualValues(t, 2, metricsChore.Counter.InlineObjects)
		require.EqualValues(t, 2, metricsChore.Counter.RemoteObjects)

		require.EqualValues(t, 2, metricsChore.Counter.TotalInlineSegments)
		require.EqualValues(t, 2, metricsChore.Counter.TotalRemoteSegments)
		// 2 inline segments * (1024 + encryption overhead)
		require.EqualValues(t, 2080, metricsChore.Counter.TotalInlineBytes)
		// 2 remote segments * (8192 + encryption overhead)
		require.EqualValues(t, 29696, metricsChore.Counter.TotalRemoteBytes)
	})
}

func TestCounterInlineOnly(t *testing.T) {
	testplanet.Run(t, testplanet.Config{
		SatelliteCount: 1, StorageNodeCount: 4, UplinkCount: 1,
	}, func(t *testing.T, ctx *testcontext.Context, planet *testplanet.Planet) {
		satellite := planet.Satellites[0]
		ul := planet.Uplinks[0]
		metricsChore := satellite.Metrics.Chore
		metricsChore.Loop.Pause()

		// upload 2 inline files
		for i := 0; i < 2; i++ {
			testData := testrand.Bytes(memory.KiB)
			path := "/some/inline/path/" + strconv.Itoa(i)
			err := ul.Upload(ctx, satellite, "bucket", path, testData)
			require.NoError(t, err)
		}

		metricsChore.Loop.TriggerWait()
		require.EqualValues(t, 2, metricsChore.Counter.InlineObjects)
		require.EqualValues(t, 0, metricsChore.Counter.RemoteObjects)
	})
}

func TestCounterRemoteOnly(t *testing.T) {
	testplanet.Run(t, testplanet.Config{
		SatelliteCount: 1, StorageNodeCount: 4, UplinkCount: 1,
		Reconfigure: testplanet.Reconfigure{
			Satellite: testplanet.MaxSegmentSize(150 * memory.KiB),
		},
	}, func(t *testing.T, ctx *testcontext.Context, planet *testplanet.Planet) {
		satellite := planet.Satellites[0]
		ul := planet.Uplinks[0]
		metricsChore := satellite.Metrics.Chore
		metricsChore.Loop.Pause()

		// upload 2 remote files with multiple segments
		for i := 0; i < 2; i++ {
			testData := testrand.Bytes(300 * memory.KiB)
			path := "/some/remote/path/" + strconv.Itoa(i)
			err := ul.Upload(ctx, satellite, "testbucket", path, testData)
			require.NoError(t, err)
		}

		metricsChore.Loop.TriggerWait()
		require.EqualValues(t, 0, metricsChore.Counter.InlineObjects)
		require.EqualValues(t, 2, metricsChore.Counter.RemoteObjects)
	})
}
