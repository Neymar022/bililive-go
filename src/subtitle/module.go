package subtitle

import (
	"context"
	"sync"
	"time"

	"github.com/bililive-go/bililive-go/src/configs"
	bilisentry "github.com/bililive-go/bililive-go/src/pkg/sentry"
	"github.com/sirupsen/logrus"
)

type Module struct {
	cancel context.CancelFunc
	wg     sync.WaitGroup
	ticker *time.Ticker
}

func NewModule() *Module {
	return &Module{}
}

func (m *Module) Start(ctx context.Context) error {
	cfg := configs.GetCurrentConfig()
	if cfg == nil || !cfg.Subtitle.Enabled {
		return nil
	}

	moduleCtx, cancel := context.WithCancel(ctx)
	m.cancel = cancel
	m.ticker = time.NewTicker(12 * time.Hour)

	m.wg.Add(1)
	bilisentry.GoWithContext(moduleCtx, func(ctx context.Context) {
		defer m.wg.Done()
		m.cleanup(moduleCtx)
		for {
			select {
			case <-ctx.Done():
				return
			case <-m.ticker.C:
				m.cleanup(ctx)
			}
		}
	})

	return nil
}

func (m *Module) Close(ctx context.Context) {
	if m.cancel != nil {
		m.cancel()
	}
	if m.ticker != nil {
		m.ticker.Stop()
	}
	m.wg.Wait()
}

func (m *Module) cleanup(ctx context.Context) {
	cfg := configs.GetCurrentConfig()
	if cfg == nil || !cfg.Subtitle.Enabled {
		return
	}
	sourceRoot := cfg.Subtitle.GetEffectiveSourceRoot(cfg.OutPutPath)
	libraryRoot := cfg.Subtitle.GetEffectiveLibraryRoot(cfg.OutPutPath)
	deleted, err := CleanupExpiredSources(libraryRoot, sourceRoot, cfg.Subtitle.RetentionDays, time.Now().UTC())
	if err != nil {
		logrus.WithError(err).Warn("字幕源文件清理失败")
		return
	}
	if deleted > 0 {
		logrus.WithField("deleted", deleted).Info("字幕清理已删除过期源文件")
	}
}
