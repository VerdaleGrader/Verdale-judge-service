// benchcompare compares two timing benchmark reports (GRD-42):
//
//	go run ./cmd/benchcompare [-threshold 10] before.json after.json
//
// It exits with status 1 when any program's median time alone moved by more
// than the threshold, which means time limits set with the old numbers need
// a second look.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/kanonkc/verdale-judge-service/pkg/benchreport"
)

func main() {
	threshold := flag.Float64("threshold", 10, "change in percent that counts as a difference")
	flag.Parse()
	if flag.NArg() != 2 {
		fmt.Fprintln(os.Stderr, "usage: benchcompare [-threshold 10] before.json after.json")
		os.Exit(2)
	}
	before, err := benchreport.Load(flag.Arg(0))
	exitOnError(err)
	after, err := benchreport.Load(flag.Arg(1))
	exitOnError(err)

	fmt.Printf("before: %s (%s)\nafter:  %s (%s)\n\n", before.Label, before.Machine.CPUModel, after.Label, after.Machine.CPUModel)
	exceeded := false
	for _, difference := range benchreport.Compare(before, after, *threshold) {
		flagText := ""
		if difference.Exceeds {
			flagText, exceeded = "  <-- beyond the threshold", true
		}
		fmt.Printf("%-16s %5d ms -> %5d ms  %+6.1f%%%s\n", difference.Name, difference.BeforeMs, difference.AfterMs, difference.ChangePct, flagText)
	}
	if exceeded {
		os.Exit(1)
	}
}

func exitOnError(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
}
