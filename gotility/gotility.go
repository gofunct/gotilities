package gotility

import (
	"github.com/gofunct/gotilities/instrument/probe"
	"github.com/gofunct/gotilities/instrument/tracer"
	"github.com/gofunct/gotilities/instrument/zapper"
	"github.com/gofunct/gotilities/rpc"
	"github.com/gofunct/gotilities/syncer"
)

type Gotility struct {
	syncer.Syncer
	zapper.Zapper
	tracer.Tracer
	probe.Prober
	rpc.Server
	rpc.Client
}
