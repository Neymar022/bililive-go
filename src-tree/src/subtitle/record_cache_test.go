package subtitle

import (
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 包级全局缓存让多个测试间共享状态——每个测试 setup 时先清掉，避免相互污染。
func resetRecordCache(t *testing.T) {
	t.Helper()
	InvalidateRecordCache()
	t.Cleanup(InvalidateRecordCache)
}

// 验证第二次 ListRecords 命中缓存：在两次调用之间往 library 里再写一个 mp4，
// 不调 invalidate 时第二次 List 看到的仍是旧结果。
func TestListRecordsCachesUntilInvalidated(t *testing.T) {
	resetRecordCache(t)

	libraryRoot := t.TempDir()
	sourceRoot := t.TempDir()

	first := filepath.Join(libraryRoot, "first.mp4")
	require.NoError(t, os.WriteFile(first, []byte("a"), 0o644))

	got, err := ListRecords(libraryRoot, sourceRoot, 7)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "first.mp4", got[0].RelativePath)

	// 缓存命中后即使磁盘多了文件，list 也应返回旧结果。
	second := filepath.Join(libraryRoot, "second.mp4")
	require.NoError(t, os.WriteFile(second, []byte("b"), 0o644))

	got, err = ListRecords(libraryRoot, sourceRoot, 7)
	require.NoError(t, err)
	assert.Len(t, got, 1, "未 invalidate 时应继续命中缓存，看不到新写入的 second.mp4")

	// 显式 invalidate 后，下次 list 应触发重建并看到新文件。
	InvalidateRecordCache()
	got, err = ListRecords(libraryRoot, sourceRoot, 7)
	require.NoError(t, err)
	require.Len(t, got, 2)
	paths := []string{got[0].RelativePath, got[1].RelativePath}
	assert.Contains(t, paths, "first.mp4")
	assert.Contains(t, paths, "second.mp4")
}

// 缓存返回的 slice 是独立副本——调用方修改不应影响下一次返回。
func TestListRecordsReturnsIndependentSlice(t *testing.T) {
	resetRecordCache(t)

	libraryRoot := t.TempDir()
	sourceRoot := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(libraryRoot, "x.mp4"), []byte("x"), 0o644))

	first, err := ListRecords(libraryRoot, sourceRoot, 7)
	require.NoError(t, err)
	require.Len(t, first, 1)

	// 改第一份返回的字段，不应影响 cache 内部状态。
	first[0].Status = StatusFailed
	first[0].LastError = "调用方乱改不应该传染缓存"

	second, err := ListRecords(libraryRoot, sourceRoot, 7)
	require.NoError(t, err)
	require.Len(t, second, 1)
	assert.Equal(t, StatusIdle, second[0].Status, "缓存返回的应是独立副本")
	assert.Empty(t, second[0].LastError)
}

// 切换 libraryRoot（如测试切 TempDir 或运行时改配置）应让 cache 自动失效，
// 不能错读上一个 root 的旧列表。
func TestListRecordsRefreshesWhenRootChanges(t *testing.T) {
	resetRecordCache(t)

	rootA := t.TempDir()
	rootB := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(rootA, "a.mp4"), []byte("a"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(rootB, "x.mp4"), []byte("x"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(rootB, "y.mp4"), []byte("y"), 0o644))

	got, err := ListRecords(rootA, t.TempDir(), 7)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "a.mp4", got[0].RelativePath)

	// 切换到 rootB——cache key 不同，应触发重建而不是复用 rootA 的结果。
	got, err = ListRecords(rootB, t.TempDir(), 7)
	require.NoError(t, err)
	require.Len(t, got, 2, "切换 libraryRoot 应自动重建，看到 rootB 的 2 个文件")
}

// 并发未命中场景：N 个 goroutine 同时 List 空 cache。配合 -race 验证无数据竞争，
// 同时确认所有 goroutine 都拿到一致结果。
func TestListRecordsConcurrentMissReturnsConsistent(t *testing.T) {
	resetRecordCache(t)

	libraryRoot := t.TempDir()
	sourceRoot := t.TempDir()
	for _, name := range []string{"a.mp4", "b.mp4", "c.mp4"} {
		require.NoError(t, os.WriteFile(filepath.Join(libraryRoot, name), []byte("x"), 0o644))
	}

	const goroutines = 16
	var wg sync.WaitGroup
	results := make([][]Record, goroutines)
	errs := make([]error, goroutines)
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func(idx int) {
			defer wg.Done()
			results[idx], errs[idx] = ListRecords(libraryRoot, sourceRoot, 7)
		}(i)
	}
	wg.Wait()

	for i := 0; i < goroutines; i++ {
		require.NoError(t, errs[i])
		require.Len(t, results[i], 3)
	}
}
