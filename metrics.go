package gotilities

import "github.com/piotrkowalczuk/promgrpc"

func (g *Gotility) MakeMetrics(trackpeers bool) *promgrpc.Interceptor {
	return promgrpc.NewInterceptor(promgrpc.InterceptorOpts{})
}
