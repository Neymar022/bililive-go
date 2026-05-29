package subtitle

import "sync"

// recordCache 是 ListRecords 的进程内索引。
//
// 为什么需要它：原 ListRecords 每次都 walk 整个 library_root，再对每个 mp4
// 调 buildRecord → sourceExistsForVideo → ResolveSourcePath → walk 整个 source_root。
// 100 个录制 × 100 个源 ≈ 10000 次 stat，HTTP 列表请求 P99 走到秒级。
//
// 设计取舍：
//   - 不维护"原地增量更新"——pipeline/rerun/delete 后整批 invalidate，下次读重建。
//     增量更新要在 buildRecord 多个分支同步状态，工程量大且容易漏。
//   - 重建走的就是原 walk 实现，所以正确性与单次 walk 严格等价；本 cache 仅影响
//     重复 list 的延迟，不改变结果。
//   - 写入路径调用 InvalidateRecordCache 标记失效；下一次读触发一次性重建。
//     对"写少读多"的字幕浏览场景这是最划算的。
type recordCacheKey struct {
	libraryRoot   string
	sourceRoot    string
	retentionDays int
}

type recordCache struct {
	mu      sync.RWMutex
	records []Record
	key     recordCacheKey
	valid   bool
}

var globalRecordCache = &recordCache{}

// listFromCacheOrRebuild 优先从缓存读；命中返回缓存副本（O(N) 内存拷贝）。
// 未命中时升级写锁走一次 walk 填充——并发未命中时只有一个 goroutine 真正 walk，
// 其余在锁内等待并复用结果。
//
// 参数三元组同时作为 cache key：参数变化（测试切换 TempDir、运行时 PUT settings
// 改 root）会让 cache 自动失效，避免错读旧库的列表。
func (c *recordCache) listFromCacheOrRebuild(libraryRoot, sourceRoot string, retentionDays int) ([]Record, error) {
	wantKey := recordCacheKey{libraryRoot: libraryRoot, sourceRoot: sourceRoot, retentionDays: retentionDays}

	c.mu.RLock()
	if c.valid && c.key == wantKey {
		out := make([]Record, len(c.records))
		copy(out, c.records)
		c.mu.RUnlock()
		return out, nil
	}
	c.mu.RUnlock()

	c.mu.Lock()
	defer c.mu.Unlock()
	// 锁升级期间可能已有别的 goroutine 完成重建——再查一次。
	if c.valid && c.key == wantKey {
		out := make([]Record, len(c.records))
		copy(out, c.records)
		return out, nil
	}

	records, err := walkRecords(libraryRoot, sourceRoot, retentionDays)
	if err != nil {
		return nil, err
	}
	c.records = records
	c.key = wantKey
	c.valid = true
	out := make([]Record, len(records))
	copy(out, records)
	return out, nil
}

func (c *recordCache) invalidate() {
	c.mu.Lock()
	c.records = nil
	c.key = recordCacheKey{}
	c.valid = false
	c.mu.Unlock()
}

// InvalidateRecordCache 标记 ListRecords 缓存失效。
// 调用时机：pipeline 字幕阶段完成、HTTP rerun 入队、源文件删除、保留标记切换、
// 字幕配置 PUT 变更等任何会让"列表数据"过时的写入路径。
//
// 调用本函数本身很便宜（仅一次锁更新），不会触发 walk——下一次 ListRecords
// 才会重建。所以可以在写入路径放心多调（多次 invalidate 等价于一次）。
func InvalidateRecordCache() {
	globalRecordCache.invalidate()
}
