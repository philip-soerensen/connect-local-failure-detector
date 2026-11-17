// Command failure-detector is a Prusa Connect Local module (runtime: service)
// that watches printer cameras during prints and detects failed prints
// ("spaghetti") with an on-device ONNX model, in the style of Obico's
// The Spaghetti Detective.
//
// It speaks the plugin wire protocol on stdin/stdout: the host launches it,
// and it answers plugin.info, plugin.configure, routes.serve and
// bus.subscribe calls, reads camera frames out of the server's storages,
// publishes <namespace>/failure.alert events, and can pause the printer.
package main

import (
	"context"
	_ "embed"
	"fmt"
	"os"
	"os/signal"

	"github.com/prusa3d/prusa-connect/sdk/extension"
	"github.com/prusa3d/prusa-connect/sdk/routesapi"
	"github.com/prusa3d/prusa-connect/sdk/wire"
	"github.com/prusa3d/prusa-connect/sdk/wirerpc"
)

const (
	name    = "failure-detector"
	release = extension.Version("0.3.20")
)

//go:embed openapi.json
var openAPI []byte

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", name, err)
		os.Exit(1)
	}
}

// run writes the handshake line on stdout, then answers the host's calls until
// the host closes stdin.
func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	line, err := wire.Handshake{
		Protocol: wire.ProtocolMajor,
		MinMinor: wire.MinMinor,
		MaxMinor: wire.MaxMinor,
		Address:  wire.StdioAddress,
	}.Line()
	if err != nil {
		return err
	}
	if _, err = os.Stdout.WriteString(line); err != nil {
		return err
	}

	serves := wirerpc.Methods{}

	conn, err := wirerpc.NewConn(stdio{}, wirerpc.Options{Role: wirerpc.RoleChild, Methods: serves})
	if err != nil {
		return err
	}

	app := newApp()

	serves[wirerpc.MethodInfo] = func(context.Context, wirerpc.Call) (any, error) {
		return routesapi.Describe(wirerpc.Info{
			Name:    name,
			Version: release,
			Contract: wirerpc.Contract,
			Serves:  []wirerpc.Method{routesapi.MethodServe, wirerpc.MethodConfigure},
		}, openAPI)
	}
	serves[wirerpc.MethodShutdown] = func(context.Context, wirerpc.Call) (any, error) {
		return nil, nil
	}
	serves[wirerpc.MethodConfigure] = wirerpc.ConfigureGranted(func(ctx context.Context, held config, grants wirerpc.Grants) error {
		return app.configure(ctx, held, grants.Data)
	})
	serves[routesapi.MethodServe] = routesapi.Serve(app.routes(conn))

	go app.watch(ctx, conn)

	return conn.Serve(ctx)
}

// stdio is the channel the host handed this process.
type stdio struct{}

func (stdio) Read(p []byte) (int, error)  { return os.Stdin.Read(p) }
func (stdio) Write(p []byte) (int, error) { return os.Stdout.Write(p) }
func (stdio) Close() error                { return os.Stdin.Close() }
