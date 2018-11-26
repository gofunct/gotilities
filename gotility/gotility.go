package gotility

import (
	"github.com/gofunct/gotilities/probe"
	"github.com/gofunct/gotilities/rpc"
	"github.com/gofunct/gotilities/syncer"
	"github.com/gofunct/gotilities/tracer"
	"github.com/gofunct/gotilities/zapper"
)

type Gotility struct {
	syncer.Syncer
	zapper.Zapper
	tracer.Tracer
	probe.Prober
	rpc.Server
	rpc.Client
}
