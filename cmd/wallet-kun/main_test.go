package main

import (
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestRunRejectsInvalidDatabaseConfiguration(t *testing.T) {
	for _, tc := range []struct{ name, url, want string }{
		{"missing URL", "", "DATABASE_URL is required"},
		{"malformed URL", "postgres://user:private-password@%zz/wallet", "invalid database configuration"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mode := gin.Mode()
			t.Cleanup(func() { gin.SetMode(mode) })
			t.Setenv("DATABASE_URL", tc.url)
			err := run()
			if err == nil || err.Error() != tc.want {
				t.Fatalf("error=%v, want %q", err, tc.want)
			}
			if strings.Contains(err.Error(), "private-password") {
				t.Fatal("startup error exposed database credentials")
			}
		})
	}
}
