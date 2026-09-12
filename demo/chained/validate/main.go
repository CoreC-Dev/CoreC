package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/CoreC-Dev/CoreC/config"

	// Import all drivers and transports for registry validation
	_ "github.com/CoreC-Dev/CoreC/driver/all"
	_ "github.com/CoreC-Dev/CoreC/transport/all"
)

func main() {
	root, _ := os.Getwd()
	pattern := filepath.Join(root, "demo", "chained", "scenario*", "*.yaml")
	files, _ := filepath.Glob(pattern)

	if len(files) == 0 {
		fmt.Println("No config files found")
		os.Exit(1)
	}

	failed := 0
	for _, f := range files {
		rel, _ := filepath.Rel(root, f)
		_, err := config.Load(f)
		if err != nil {
			fmt.Printf("FAIL  %s — %v\n", rel, err)
			failed++
		} else {
			fmt.Printf("OK    %s\n", rel)
		}
	}

	fmt.Printf("\n%d/%d configs valid\n", len(files)-failed, len(files))
	if failed > 0 {
		os.Exit(1)
	}

	// Also validate the main config.example.yaml
	example := filepath.Join(root, "config.example.yaml")
	if _, err := config.Load(example); err != nil {
		fmt.Printf("FAIL  config.example.yaml — %v\n", err)
		os.Exit(1)
	}
	fmt.Println("OK    config.example.yaml")
	_ = strings.TrimSpace
}
