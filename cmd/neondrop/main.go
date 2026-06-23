package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/good-atom/neondrop/internal/server"
)

func main() {
	addr := flag.String("addr", ":8080", "HTTP listen address")
	dataDir := flag.String("data", defaultDataDir(), "directory for temporary transfers")
	flag.Parse()

	app, err := server.New(*dataDir)
	if err != nil {
		log.Fatal(err)
	}
	defer app.Close()

	httpServer := &http.Server{
		Addr:              *addr,
		Handler:           app.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       90 * time.Second,
	}

	printURLs(*addr)

	errs := make(chan error, 1)
	go func() {
		errs <- httpServer.ListenAndServe()
	}()

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)

	select {
	case sig := <-signals:
		log.Printf("received %s, shutting down", sig)
		_ = app.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		if err := httpServer.Shutdown(ctx); err != nil {
			log.Printf("graceful shutdown failed: %v", err)
		}
	case err := <-errs:
		if err != nil && err != http.ErrServerClosed {
			log.Fatal(err)
		}
	}
}

func defaultDataDir() string {
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		return filepath.Join(".", "data")
	}
	return filepath.Join(cacheDir, "neondrop")
}

func printURLs(addr string) {
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		port = "8080"
	}

	fmt.Printf("\nNeonDrop is running\n")
	fmt.Printf("  This device: http://localhost:%s\n", port)

	ifaces, err := net.Interfaces()
	if err == nil {
		for _, iface := range ifaces {
			if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 ||
				iface.Flags&net.FlagPointToPoint != 0 || isTunnelInterface(iface.Name) {
				continue
			}
			addrs, _ := iface.Addrs()
			for _, candidate := range addrs {
				ip, _, err := net.ParseCIDR(candidate.String())
				if err == nil && ip.To4() != nil {
					fmt.Printf("  Local network: http://%s:%s\n", ip.String(), port)
				}
			}
		}
	}
	fmt.Println("\nOpen a local-network URL on devices connected to the same Wi-Fi.")
}

func isTunnelInterface(name string) bool {
	name = strings.ToLower(name)
	for _, prefix := range []string{"utun", "tun", "tap", "wg", "ppp", "ipsec"} {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}
