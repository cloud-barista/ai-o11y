package rest

import (
	"crypto/subtle"
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"

	"github.com/cloud-barista/ai-o11y/internal/model"
)

// openPaths are reachable without a token: readiness probes carry no
// credentials.
var openPaths = []string{BasePath + "/readyz"}

// openPrefixes are the open path trees. The Swagger UI loads its assets from
// here and asks for the token rather than holding it.
var openPrefixes = []string{BasePath + "/api/"}

// RequireBearer rejects a request that does not carry
// "Authorization: Bearer <token>" with 401. An empty token turns the check off.
func RequireBearer(token string) echo.MiddlewareFunc {
	open := map[string]bool{}
	for _, path := range openPaths {
		open[path] = true
	}
	want := []byte(token)

	return func(next echo.HandlerFunc) echo.HandlerFunc {
		if token == "" {
			return next
		}
		return func(c echo.Context) error {
			// The path the router matched on, so an encoded path cannot read
			// as open here and reach a protected route there.
			if isOpen(echo.GetPath(c.Request()), open) {
				return next(c)
			}
			if subtle.ConstantTimeCompare(bearerToken(c.Request()), want) != 1 {
				c.Response().Header().Set(echo.HeaderWWWAuthenticate, "Bearer")
				return c.JSON(http.StatusUnauthorized, model.SimpleMsg{Message: "Unauthorized"})
			}
			return next(c)
		}
	}
}

func isOpen(path string, open map[string]bool) bool {
	if open[path] {
		return true
	}
	for _, prefix := range openPrefixes {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return false
}

// bearerToken returns the credentials of a Bearer Authorization header, or nil.
func bearerToken(r *http.Request) []byte {
	scheme, credentials, ok := strings.Cut(r.Header.Get(echo.HeaderAuthorization), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return nil
	}
	return []byte(strings.TrimSpace(credentials))
}
