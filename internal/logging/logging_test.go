package logging_test

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"wallet-kun/internal/logging"
)

func preserveLogger(t *testing.T) {
	t.Helper()
	original := slog.Default()
	t.Cleanup(func() { slog.SetDefault(original) })
}

func assertJSONLog(t *testing.T, filename string) {
	t.Helper()
	data, err := os.ReadFile(filename)
	if err != nil {
		t.Fatal(err)
	}
	var entry map[string]any
	if err := json.Unmarshal(data, &entry); err != nil {
		t.Fatalf("invalid structured log: %s (%v)", data, err)
	}
	if entry["msg"] != "test event" || entry["level"] != "INFO" || entry["operation"] != "debit" || entry["time"] == nil {
		t.Fatalf("unexpected log fields: %v", entry)
	}
}

func TestProductionLogging(t *testing.T) {
	for _, tc := range []struct{ name, env, filename string }{
		{"default environment and path", "", ""},
		{"explicit production", "production", ""},
		{"custom nested path", "production", filepath.Join("custom", "nested", "service.log")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			preserveLogger(t)
			t.Chdir(t.TempDir())
			closeLog, err := logging.Setup(tc.env, tc.filename)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := closeLog(); err != nil {
					t.Error(err)
				}
			})
			slog.Info("test event", "operation", "debit")
			filename := tc.filename
			if filename == "" {
				filename = filepath.Join("logs", "wallet-kun.log")
			}
			assertJSONLog(t, filename)
		})
	}
}

func TestDevelopmentLoggingUsesConsole(t *testing.T) {
	preserveLogger(t)
	dir := t.TempDir()
	console, err := os.Create(filepath.Join(dir, "console.txt"))
	if err != nil {
		t.Fatal(err)
	}
	original := os.Stdout
	os.Stdout = console
	t.Cleanup(func() { os.Stdout = original; console.Close() })
	unusedPath := filepath.Join(dir, "unused", "service.log")
	closeLog, err := logging.Setup("development", unusedPath)
	if err != nil {
		t.Fatal(err)
	}
	slog.Info("test event", "operation", "debit")
	if err := closeLog(); err != nil {
		t.Fatal(err)
	}
	assertJSONLog(t, console.Name())
	if _, err := os.Stat(filepath.Dir(unusedPath)); !os.IsNotExist(err) {
		t.Fatalf("development logging touched the file output path: %v", err)
	}
}

func TestLoggingSetupFailuresPreserveExistingLogger(t *testing.T) {
	for _, name := range []string{"unknown environment", "directory is a file", "log path is a directory"} {
		t.Run(name, func(t *testing.T) {
			preserveLogger(t)
			original := slog.Default()
			env := "production"
			filename := filepath.Join(t.TempDir(), "output")
			switch name {
			case "unknown environment":
				env = "staging"
			case "directory is a file":
				if err := os.WriteFile(filename, []byte("occupied"), 0600); err != nil {
					t.Fatal(err)
				}
				filename = filepath.Join(filename, "service.log")
			case "log path is a directory":
				if err := os.Mkdir(filename, 0700); err != nil {
					t.Fatal(err)
				}
			}
			closeLog, err := logging.Setup(env, filename)
			if err == nil {
				if closeLog != nil {
					_ = closeLog()
				}
				t.Fatal("expected setup failure")
			}
			if closeLog != nil {
				t.Fatal("failed setup returned a cleanup callback")
			}
			if slog.Default() != original {
				t.Fatal("failed setup replaced the existing logger")
			}
		})
	}
}
