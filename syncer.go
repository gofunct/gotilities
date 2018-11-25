package gotilities

import (
	"context"
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
