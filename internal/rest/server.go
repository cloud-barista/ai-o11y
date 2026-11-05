// Package rest wires the Echo HTTP server and its routes.
package rest

import (
	"context"
	"fmt"
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	echoSwagger "github.com/swaggo/echo-swagger"

	_ "github.com/cloud-barista/ai-o11y/api/docs"

	"github.com/cloud-barista/ai-o11y/internal/config"
)

// BasePath is the mount point for every route of this service.
const BasePath = "/aio11y"

// Server owns the Echo instance and its lifecycle.
type Server struct {
	echo *echo.Echo
	port int
}

// NewServer builds the AI-O11y service. Monitoring routes (targets, collector
// configuration, queries) are added here as their packages land.
func NewServer(cfg *config.Config) *Server {
	e := echo.New()
	e.HideBanner = true
	e.HidePort = true
	e.Use(middleware.Recover())
	e.Use(middleware.RequestID())
	e.Use(RequireBearer(cfg.Auth.Token))

	g := e.Group(BasePath)
	g.GET("/readyz", RestGetReadyz)
	g.GET("/api/*", echoSwagger.WrapHandler)

	return &Server{echo: e, port: cfg.Server.Port}
}

// Start blocks serving HTTP until the server is shut down.
func (s *Server) Start() error {
	if err := s.echo.Start(fmt.Sprintf(":%d", s.port)); err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("http server stopped: %w", err)
	}
	return nil
}

// Shutdown gracefully stops the server.
func (s *Server) Shutdown(ctx context.Context) error {
	if err := s.echo.Shutdown(ctx); err != nil {
		return fmt.Errorf("failed to shut down http server: %w", err)
	}
	return nil
}

// Port reports the port the server listens on.
func (s *Server) Port() int {
	return s.port
}
