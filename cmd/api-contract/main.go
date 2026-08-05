package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/owainlewis/factory/internal/controlplane"
)

func main() {
	compatibility := flag.Bool("compatibility-baseline", false, "write the compatibility baseline instead of Markdown")
	checkCompatibility := flag.String("check-compatibility", "", "check the live contract against a baseline file")
	ratchetCompatibility := flag.String("ratchet-compatibility", "", "verify an existing baseline and write the current compatible baseline")
	flag.Parse()
	selectedModes := 0
	for _, selected := range []bool{*compatibility, *checkCompatibility != "", *ratchetCompatibility != ""} {
		if selected {
			selectedModes++
		}
	}
	if selectedModes > 1 {
		fmt.Fprintln(os.Stderr, "api-contract: choose one mode")
		os.Exit(2)
	}
	baselinePath := *checkCompatibility
	if baselinePath == "" {
		baselinePath = *ratchetCompatibility
	}
	if baselinePath != "" {
		encoded, err := os.ReadFile(baselinePath)
		if err != nil {
			fmt.Fprintln(os.Stderr, "api-contract:", err)
			os.Exit(1)
		}
		check := controlplane.CheckAPICompatibilityBaseline
		if *ratchetCompatibility != "" {
			check = controlplane.CheckAPICompatibility
		}
		if err := check(encoded); err != nil {
			fmt.Fprintln(os.Stderr, "api-contract:", err)
			os.Exit(1)
		}
		if *ratchetCompatibility != "" {
			if _, err := os.Stdout.Write(controlplane.RenderAPICompatibilityBaseline()); err != nil {
				panic(err)
			}
		}
		return
	}
	output := controlplane.RenderAPIContract()
	if *compatibility {
		output = controlplane.RenderAPICompatibilityBaseline()
	}
	if _, err := os.Stdout.Write(output); err != nil {
		panic(err)
	}
}
