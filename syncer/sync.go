package syncer

import (
	"context"
	"sync"
	"time"
)

type SyncerUtil interface {
	MakeErrGrp() (*Group, context.Context)
	MakeErrGrpWithDeadline(duration time.Duration) (context.Context, context.CancelFunc, *Group)
}

type Syncer struct{}

func (g *Syncer) MakeErrGrp() (*Group, context.Context) {
	return WithContext(context.Background())
}

func (g *Syncer) MakeErrGrpWithDeadline(duration time.Duration) (context.Context, context.CancelFunc, *Group) {
	d := time.Now().Add(duration * time.Second)

	ctx, cancel := context.WithDeadline(context.Background(), d)

	grp, ctx := WithContext(ctx)
	return ctx, cancel, grp
}

type Group struct {
	cancel func()

	wg sync.WaitGroup

	errOnce sync.Once
	err     error
}

func WithContext(ctx context.Context) (*Group, context.Context) {
	ctx, cancel := context.WithCancel(ctx)
	return &Group{cancel: cancel}, ctx
}

func (g *Group) Wait() error {
	g.wg.Wait()
	if g.cancel != nil {
		g.cancel()
	}
	return g.err
}

func (g *Group) Go(f func() error) {
	g.wg.Add(1)

	go func() {
		defer g.wg.Done()

		if err := f(); err != nil {
			g.errOnce.Do(func() {
				g.err = err
				if g.cancel != nil {
					g.cancel()
				}
			})
		}
	}()
}
