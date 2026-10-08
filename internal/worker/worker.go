package worker

import (
	"context"
	"log/slog"
	"time"
)

type Expirer interface {
	Expire(context.Context) (int64, error)
}

func Run(ctx context.Context, repo Expirer, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			jobCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			n, err := repo.Expire(jobCtx)
			cancel()
			if err != nil && ctx.Err() == nil {
				slog.Error("не удалось закрыть просроченные брони", "error", err)
			}
			if n > 0 {
				slog.Info("просроченные брони закрыты", "count", n)
			}
		}
	}
}
