// evalsetdiff — v0.10.666 (CoSRE değerlendirme Faz A): iki evalset koşum
// artefaktını (COREMETRY_EVAL_OUT dizini) diff'ler.
//
//	go run ./cmd/evalsetdiff evalset-runs/evalset-A.json evalset-runs/evalset-B.json
//	go run ./cmd/evalsetdiff -latest evalset-runs   # dizindeki son iki koşum
//
// Çıkış kodu: 0 = gerileme yok, 2 = en az bir vaka geriledi ya da yeni FAIL
// var (CI dışı; geliştirici komut satırı için sinyal). Geliştirici aracı;
// release imajına girmez.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/cilcenk/coremetry/internal/ai/evalrubric"
)

func main() {
	latest := flag.String("latest", "", "dizin: içindeki son iki koşumu kıyasla")
	flag.Parse()
	var a, b string
	switch {
	case *latest != "":
		files, _ := filepath.Glob(filepath.Join(*latest, "evalset-*.json"))
		sort.Strings(files) // ad unix saniyeyle başlar → kronolojik
		if len(files) < 2 {
			fmt.Fprintln(os.Stderr, "en az iki koşum gerekir")
			os.Exit(1)
		}
		a, b = files[len(files)-2], files[len(files)-1]
	case flag.NArg() == 2:
		a, b = flag.Arg(0), flag.Arg(1)
	default:
		fmt.Fprintln(os.Stderr, "kullanım: evalsetdiff <önce.json> <sonra.json> | -latest <dizin>")
		os.Exit(1)
	}
	before, err := evalrubric.ReadRun(a)
	if err != nil {
		fmt.Fprintln(os.Stderr, a+":", err)
		os.Exit(1)
	}
	after, err := evalrubric.ReadRun(b)
	if err != nil {
		fmt.Fprintln(os.Stderr, b+":", err)
		os.Exit(1)
	}
	rep := evalrubric.Diff(before, after)
	fmt.Print(evalrubric.FormatDiff(before, after, rep))
	if len(rep.Regressed) > 0 || len(rep.NewlyFailing) > 0 {
		os.Exit(2)
	}
}
