//go:build linux

package stages

import (
	"context"
	"github.com/bililive-go/bililive-go/src/subtitle"
	"github.com/stretchr/testify/require"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLegacyAggregateHonorsPersistentSeasonGate(t *testing.T) {
	stubLiveSessionMediaForEpisodeList(t, []float64{3, 3}, []string{"S01E0018", "S01E0019"})
	stubLiveSessionCoverExtraction(t, nil)
	root := t.TempDir()
	library := filepath.Join(root, "video")
	first, firstMeta, _ := writeCompletedKnowledgeSessionSidecarAt(t, library, "Host", 18, time.Date(2026, 6, 1, 18, 18, 0, 0, time.UTC), "字幕1")
	second, secondMeta, _ := writeCompletedKnowledgeSessionSidecarAt(t, library, "Host", 19, time.Date(2026, 6, 1, 18, 19, 0, 0, time.UTC), "字幕2")
	manifest := knowledgeSessionManifest{LiveSessionID: "legacy", Sources: []knowledgeSessionManifestSource{{LibraryPath: first, MetadataPath: firstMeta}, {LibraryPath: second, MetadataPath: secondMeta}}}
	marker := subtitle.LibraryRecoveryMarker(first)
	require.NoError(t, os.MkdirAll(filepath.Dir(marker), 0700))
	require.NoError(t, os.WriteFile(marker, []byte("external-index-conflict"), 0600))
	require.Error(t, subtitle.AssertLibraryPublicationReady(first))
	aggregate, err := publishLiveSessionMediaAggregate(context.Background(), "", library, &manifest)
	_, firstErr := os.Stat(first)
	_, secondErr := os.Stat(second)
	t.Logf("legacy aggregate_created=%v error=%v original_media_missing=%v/%v persistent_gate=%v", aggregate != nil, err, os.IsNotExist(firstErr), os.IsNotExist(secondErr), subtitle.AssertLibraryPublicationReady(first))
	require.Error(t, err, "legacy aggregate writer must not pass a persisted Season recovery/conflict gate")
}
