package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"

	"github.com/Life-USTC/Bot/internal/specification"
)

func main() {
	input := flag.String("input", "", "native go test -json event file")
	output := flag.String("output", "", "validated semantic evidence JSON")
	flag.Parse()
	if *input == "" || *output == "" {
		fmt.Fprintln(os.Stderr, "-input and -output are required")
		os.Exit(2)
	}
	if err := run(*input, *output); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run(input, output string) error {
	status, err := exec.Command("git", "-C", specification.Root(), "status", "--porcelain").Output()
	if err != nil {
		return err
	}
	if len(status) != 0 {
		return fmt.Errorf("semantic evidence requires a clean committed checkout")
	}
	catalog, err := specification.Load()
	if err != nil {
		return err
	}
	file, err := os.Open(input)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	summary, err := specification.ValidateEvidence(catalog, file)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(summary, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(output, append(data, '\n'), 0644); err != nil {
		return err
	}
	fmt.Printf("%d requirements / %d native semantic cases verified on %s\n", summary.Requirements, summary.Cases, summary.Commit)
	return nil
}
