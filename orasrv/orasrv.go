package orasrv

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"reflect"
	"sync"

	"github.com/gogo/protobuf/proto"
	"github.com/pkg/errors"
	oracall "github.com/tgulacsi/oracall/lib"

	"github.com/go-kit/kit/log"
	"github.com/oklog/ulid"

	"github.com/grpc-ecosystem/go-grpc-middleware"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	goracle "gopkg.in/goracle.v2"
)

var bufPool = sync.Pool{New: func() interface{} { return bytes.NewBuffer(make([]byte, 0, 4096)) }}

func GRPCServer(logger log.Logger, verbose bool, checkAuth func(ctx context.Context, path string) error, options ...grpc.ServerOption) *grpc.Server {
	erroredMethods := make(map[string]struct{})
	var erroredMethodsMu sync.RWMutex

	getLogger := func(ctx context.Context, fullMethod string) (log.Logger, func(error), context.Context) {
		reqID := ContextGetReqID(ctx)
		ctx = ContextWithReqID(ctx, reqID)
		logger := log.With(logger, "reqID", reqID)
		ctx = ContextWithLogger(ctx, logger)
		verbose := verbose
		var wasThere bool
		if !verbose {
			erroredMethodsMu.RLock()
			_, verbose = erroredMethods[fullMethod]
			erroredMethodsMu.RUnlock()
			wasThere = verbose
		}
		if verbose {
			ctx = goracle.ContextWithLog(ctx, log.With(logger, "lib", "goracle").Log)
		}
		commit := func(err error) {
			if wasThere && err == nil {
				erroredMethodsMu.Lock()
				delete(erroredMethods, fullMethod)
				erroredMethodsMu.Unlock()
			} else if err != nil && !wasThere {
				erroredMethodsMu.Lock()
				erroredMethods[fullMethod] = struct{}{}
				erroredMethodsMu.Unlock()
			}
		}
		return logger, commit, ctx
	}

	opts := []grpc.ServerOption{
		grpc.RPCCompressor(grpc.NewGZIPCompressor()),
		grpc.RPCDecompressor(grpc.NewGZIPDecompressor()),
		grpc.StreamInterceptor(
			func(srv interface{}, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) (err error) {
				defer func() {
					if r := recover(); r != nil {
						var ok bool
						if err, ok = r.(error); ok {
							logger.Log("PANIC", err)
							return
						}
						err = errors.Errorf("%+v", r)
						logger.Log("PANIC", fmt.Sprintf("%+v", err))
					}
				}()
				logger, commit, ctx := getLogger(ss.Context(), info.FullMethod)

				buf := bufPool.Get().(*bytes.Buffer)
				defer func() {
					buf.Reset()
					bufPool.Put(buf)
				}()
				buf.Reset()
				jenc := json.NewEncoder(buf)
				if err = jenc.Encode(srv); err != nil {
					logger.Log("marshal error", err, "srv", srv)
				}
				logger.Log("REQ", info.FullMethod, "srv", buf.String())
				if err = checkAuth(ctx, info.FullMethod); err != nil {
					return status.Error(codes.Unauthenticated, err.Error())
				}

				wss := grpc_middleware.WrapServerStream(ss)
				wss.WrappedContext = ctx
				err = handler(srv, wss)

				logger.Log("RESP", info.FullMethod, "error", err)
				commit(err)
				return StatusError(err)
			}),

		grpc.UnaryInterceptor(
			func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (resp interface{}, err error) {
				defer func() {
					if r := recover(); r != nil {
						var ok bool
						if err, ok = r.(error); ok {
							logger.Log("PANIC", err)
							return
						}
						err = errors.Errorf("%+v", r)
						logger.Log("PANIC", fmt.Sprintf("%+v", err))
					}
				}()
				logger, commit, ctx := getLogger(ctx, info.FullMethod)

				if err = checkAuth(ctx, info.FullMethod); err != nil {
					return nil, status.Error(codes.Unauthenticated, err.Error())
				}

				buf := bufPool.Get().(*bytes.Buffer)
				defer func() {
					buf.Reset()
					bufPool.Put(buf)
				}()
				buf.Reset()
				jenc := json.NewEncoder(buf)
				if err = jenc.Encode(req); err != nil {
					logger.Log("marshal error", err, "req", req)
				}
				logger.Log("REQ", info.FullMethod, "req", buf.String())

				// Fill PArgsHidden
				if r := reflect.ValueOf(req).Elem(); r.Kind() != reflect.Struct {
					logger.Log("error", "not struct", "req", fmt.Sprintf("%T %#v", req, req))
				} else {
					if f := r.FieldByName("PArgsHidden"); f.IsValid() {
						f.Set(reflect.ValueOf(buf.String()))
					}
				}

				res, err := handler(ctx, req)
				resp = res

				logger.Log("RESP", info.FullMethod, "error", err)
				commit(err)

				buf.Reset()
				if err := jenc.Encode(res); err != nil {
					logger.Log("marshal error", err, "res", res)
				}
				logger.Log("RESP", res, "error", err)

				return res, StatusError(err)
			}),
	}
	return grpc.NewServer(opts...)
}

func StatusError(err error) error {
	if err == nil {
		return err
	}
	var code codes.Code
	cerr := errors.Cause(err)
	if cerr == oracall.ErrInvalidArgument {
		code = codes.InvalidArgument
	} else if sc, ok := cerr.(interface {
		Code() codes.Code
	}); ok {
		code = sc.Code()
	}
	if code == 0 {
		return err
	}
	s := status.New(code, err.Error())
	if sd, sErr := s.WithDetails(&pbMessage{Message: fmt.Sprintf("%+v", err)}); sErr == nil {
		s = sd
	}
	return s.Err()
}

type pbMessage struct {
	Message string
}

func (m pbMessage) ProtoMessage()   {}
func (m *pbMessage) Reset()         { m.Message = "" }
func (m *pbMessage) String() string { return proto.MarshalTextString(m) }

type ctxKey string

const reqIDCtxKey = ctxKey("reqID")
const loggerCtxKey = ctxKey("logger")

func ContextWithLogger(ctx context.Context, logger log.Logger) context.Context {
	return context.WithValue(ctx, loggerCtxKey, logger)
}
func ContextGetLogger(ctx context.Context) log.Logger {
	if lgr, ok := ctx.Value(loggerCtxKey).(log.Logger); ok {
		return lgr
	}
	return nil
}
func ContextWithReqID(ctx context.Context, reqID string) context.Context {
	if reqID == "" {
		reqID = NewULID()
	}
	return context.WithValue(ctx, reqIDCtxKey, reqID)
}
func ContextGetReqID(ctx context.Context) string {
	if reqID, ok := ctx.Value(reqIDCtxKey).(string); ok {
		return reqID
	}
	return NewULID()
}
func NewULID() string {
	return ulid.MustNew(ulid.Now(), rand.Reader).String()
}