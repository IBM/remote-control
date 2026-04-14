package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	testmain "github.com/gabe-l-hart/remote-control/test"
)

func TestMain(m *testing.M) {
	testmain.TestMain(m)
}

// TestAndroidArgvDuplication tests the workaround for Android Termux
// duplicating the executable path in argv[1]
func TestAndroidArgvDuplication(t *testing.T) {
	tests := []struct {
		name         string
		initialArgs  []string
		expectedArgs []string
		description  string
	}{
		{
			name: "absolute path with duplicate in argv[1]",
			initialArgs: []string{
				"/data/data/com.termux/files/usr/bin/remote-control",
				"/data/data/com.termux/files/usr/bin/remote-control",
				"server",
			},
			expectedArgs: []string{
				"/data/data/com.termux/files/usr/bin/remote-control",
				"server",
			},
			description: "When argv[0] is absolute and argv[1] is the same absolute path, argv[1] should be removed",
		},
		{
			name: "relative path with different absolute path in argv[1]",
			initialArgs: []string{
				"./remote-control",
				"/absolute/path/to/remote-control",
				"connect",
			},
			expectedArgs: []string{
				"./remote-control",
				"/absolute/path/to/remote-control",
				"connect",
			},
			description: "When argv[0] is relative and argv[1] is a different absolute path, nothing should change",
		},
		{
			name: "no duplication - normal case",
			initialArgs: []string{
				"/usr/bin/remote-control",
				"server",
				"--port=8080",
			},
			expectedArgs: []string{
				"/usr/bin/remote-control",
				"server",
				"--port=8080",
			},
			description: "When there's no duplication, args should remain unchanged",
		},
		{
			name: "single arg - no argv[1]",
			initialArgs: []string{
				"/usr/bin/remote-control",
			},
			expectedArgs: []string{
				"/usr/bin/remote-control",
			},
			description: "When there's only argv[0], nothing should change",
		},
		{
			name: "different paths - no duplication",
			initialArgs: []string{
				"/usr/bin/remote-control",
				"/usr/bin/other-binary",
				"arg",
			},
			expectedArgs: []string{
				"/usr/bin/remote-control",
				"/usr/bin/other-binary",
				"arg",
			},
			description: "When argv[1] is a different path, nothing should change",
		},
		{
			name: "PATH-based executable with absolute duplicate in argv[1]",
			initialArgs: []string{
				"remote-control",
				"/data/data/com.termux/files/usr/bin/remote-control",
				"server",
			},
			expectedArgs: []string{
				"/data/data/com.termux/files/usr/bin/remote-control",
				"server",
			},
			description: "When argv[0] is just the executable name (from PATH) and argv[1] is the absolute path, argv[1] should be removed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Save original os.Args
			originalArgs := os.Args
			defer func() {
				os.Args = originalArgs
			}()

			// Set up test args
			os.Args = make([]string, len(tt.initialArgs))
			copy(os.Args, tt.initialArgs)

			// Apply the Android argv fix logic (matching Execute() implementation)
			if len(os.Args) > 1 {
				// Case 1: argv[0] contains a path (relative or absolute)
				if strings.ContainsRune(os.Args[0], filepath.Separator) {
					if absCmd, err := filepath.Abs(os.Args[0]); err == nil {
						if os.Args[1] == absCmd {
							os.Args = os.Args[1:]
						}
					}
				} else {
					// Case 2: argv[0] is just the executable name (from PATH)
					// Check if argv[1] is an absolute path ending with the same name
					if filepath.IsAbs(os.Args[1]) && filepath.Base(os.Args[1]) == os.Args[0] {
						os.Args = os.Args[1:]
					}
				}
			}

			// Verify the result
			if len(os.Args) != len(tt.expectedArgs) {
				t.Errorf("%s: expected %d args, got %d args\nExpected: %v\nGot: %v",
					tt.description, len(tt.expectedArgs), len(os.Args), tt.expectedArgs, os.Args)
				return
			}

			for i := range os.Args {
				if os.Args[i] != tt.expectedArgs[i] {
					t.Errorf("%s: arg[%d] mismatch\nExpected: %v\nGot: %v",
						tt.description, i, tt.expectedArgs, os.Args)
					return
				}
			}
		})
	}
}

// TestAndroidArgvDuplicationWithRelativePath specifically tests the case
// where argv[0] is a relative path and needs to be resolved to absolute
func TestAndroidArgvDuplicationWithRelativePath(t *testing.T) {
	// Save original os.Args
	originalArgs := os.Args
	defer func() {
		os.Args = originalArgs
	}()

	// Create a temporary directory and file to simulate a real executable
	tmpDir := t.TempDir()
	execName := "test-binary"
	execPath := filepath.Join(tmpDir, execName)

	// Create a dummy file
	f, err := os.Create(execPath)
	if err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}
	f.Close()

	// Change to the temp directory so relative path works
	originalWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Failed to get working directory: %v", err)
	}
	defer os.Chdir(originalWd)

	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("Failed to change directory: %v", err)
	}

	// Get the absolute path after changing directory
	absPath, err := filepath.Abs("./" + execName)
	if err != nil {
		t.Fatalf("Failed to get absolute path: %v", err)
	}

	// Test with relative path in argv[0] and absolute path in argv[1]
	os.Args = []string{
		"./" + execName, // relative path
		absPath,         // absolute path (duplicate)
		"server",
	}

	// Apply the Android argv fix logic
	if absCmd, err := filepath.Abs(os.Args[0]); err == nil {
		if len(os.Args) > 1 && os.Args[1] == absCmd {
			os.Args = os.Args[1:]
		}
	}

	// After the fix, we should have removed the duplicate
	expectedArgs := []string{
		absPath,
		"server",
	}

	if len(os.Args) != len(expectedArgs) {
		t.Errorf("Expected %d args, got %d args\nExpected: %v\nGot: %v",
			len(expectedArgs), len(os.Args), expectedArgs, os.Args)
		return
	}

	for i := range os.Args {
		if os.Args[i] != expectedArgs[i] {
			t.Errorf("arg[%d] mismatch\nExpected: %v\nGot: %v",
				i, expectedArgs, os.Args)
			return
		}
	}
}
