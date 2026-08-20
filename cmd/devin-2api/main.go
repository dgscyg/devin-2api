// 本文件负责加载配置、组装服务依赖并启动 HTTP 服务器。
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/dgscyg/devin-2api/internal/adapter"
	"github.com/dgscyg/devin-2api/internal/adapter/devin"
	"github.com/dgscyg/devin-2api/internal/app"
	"github.com/dgscyg/devin-2api/internal/config"
	"github.com/dgscyg/devin-2api/internal/dashboard"
	"github.com/dgscyg/devin-2api/internal/debuglog"
)

func main() {
	configPath := flag.String("config", "config.yaml", "YAML 配置文件路径")
	flag.Parse()

	absoluteConfigPath, err := filepath.Abs(*configPath)
	if err != nil {
		log.Fatal(err)
	}
	serviceConfig, err := config.Load(absoluteConfigPath)
	if err != nil {
		log.Fatal(err)
	}

	providerAdapter := adapter.Adapter(adapter.Unavailable{Reason: "provider adapter is not configured"})
	if serviceConfig.Devin.Token != "" {
		configured, createErr := devin.New(devin.Config{
			BaseURL:    serviceConfig.Devin.BaseURL,
			Token:      serviceConfig.Devin.Token,
			Model:      serviceConfig.Devin.Model,
			Proxy:      serviceConfig.Devin.Proxy,
			ForceHTTP1: serviceConfig.Devin.ForceHTTP1 != nil && *serviceConfig.Devin.ForceHTTP1,
		})
		if createErr != nil {
			log.Fatal(createErr)
		}
		providerAdapter = configured
	}
	var debugManager *debuglog.Manager
	if serviceConfig.Debug.Enabled {
		debugManager = debuglog.NewManager(filepath.Join(filepath.Dir(absoluteConfigPath), "logs"))
	}
	application := app.New(providerAdapter, serviceConfig.Server, debugManager)
	application.SetAPIKey(serviceConfig.Auth.APIKey)
	if serviceConfig.Devin.Token != "" {
		application.SetDashboard(dashboard.New(serviceConfig.Dashboard.Password, serviceConfig.Devin.BaseURL, serviceConfig.Devin.Token, serviceConfig.Devin.Proxy, serviceConfig.Devin.ForceHTTP1 != nil && *serviceConfig.Devin.ForceHTTP1))
	}
	server := application.HTTPServer()
	log.Printf("HTTP server listening on %s", listenURL(server.Addr))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, server); err != nil {
		log.Fatal(err)
	}
}

// listenURL 生成启动日志中的监听描述：配置为通配地址时同时给出 localhost 可访问地址。
func listenURL(listen string) string {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return listen
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		return listen + " (http://localhost:" + port + ")"
	}
	return "http://" + host + ":" + port
}

func run(ctx context.Context, server interface {
	ListenAndServe() error
	Shutdown(context.Context) error
	Close() error
}) error {
	result := make(chan error, 1)
	go func() {
		result <- server.ListenAndServe()
	}()

	select {
	case err := <-result:
		if errors.Is(err, http.ErrServerClosed) || errors.Is(err, context.Canceled) {
			return nil
		}
		return fmt.Errorf("serve HTTP: %w", err)
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		// 优雅关闭超时（可能有活跃 SSE 流），强制关闭不再报错。
		server.Close()
	}
	return nil
}
