package zapper

import (
	"context"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"google.golang.org/grpc/metadata"
)

type ZapUtil interface {
	MakeZapper(global bool) (*zap.Logger, error)
	ZapMetaData(ctx context.Context, logger *zap.Logger, fields ...zapcore.Field) *zap.Logger
	ZapErr(logger *zap.Logger, msg string, err error)
}

type Zapper struct{}

func (g *Zapper) MakeZapper(global bool) (*zap.Logger, error) {
	logger, err := zap.NewDevelopment()

	if err != nil {
		return nil, err
	}

	if global == true {
		zap.ReplaceGlobals(logger)
		logger.Debug("global zap logging enabled")
	} else {
		logger.Debug("zap logging enabled")
	}

	return logger, err
}

func (opts *Zapper) ZapMetaData(ctx context.Context, logger *zap.Logger, fields ...zapcore.Field) *zap.Logger {
	l := logger.With(fields...)
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		if rid, ok := md["request_id"]; ok && len(rid) >= 1 {
			l = l.With(zap.String("request_id", rid[0]))
		}
	}
	return l
}

func (g *Zapper) ZapErr(logger *zap.Logger, msg string, err error) {
	if err != nil {
		logger.Fatal(msg, zap.Error(err))
	}
}
