package subtitle

import "errors"

// ErrLibraryComputeRequired 表示只读成品领取不能替代需要计算的聚合。
var ErrLibraryComputeRequired = errors.New("compute_required")
