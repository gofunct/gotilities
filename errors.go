package gotilities

import (
	"context"
	"time"
)

func (g *Gotility) MakeErrGrp() (*Group, context.Context) {
	return WithContext(context.Background())
}

func (g *Gotility) MakeErrGrpWithDeadline(duration time.Duration) (context.Context, context.CancelFunc, *Group) {
	d := time.Now().Add(duration * time.Second)

	ctx, cancel := context.WithDeadline(context.Background(), d)

	grp, ctx := WithContext(ctx)
	return ctx, cancel, grp
}
