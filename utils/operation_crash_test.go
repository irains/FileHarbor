package utils

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/irains/fileharbor/conf"
)

// Run the actual operation in a disposable process: os.Exit deliberately skips
// deferred cleanup, unlike an observer returning an ordinary error.
func TestOperationCrashHelper(t *testing.T) {
	if os.Getenv("FILEHARBOR_CRASH_HELPER") != "1" {
		return
	}
	conf.FileHarbor = os.Getenv("FILEHARBOR_CRASH_ROOT")
	kind, phase := os.Getenv("FILEHARBOR_CRASH_KIND"), os.Getenv("FILEHARBOR_CRASH_PHASE")
	ctx := WithOperationObserver(context.Background(), func(event OperationEvent) error {
		if event.Phase != phase {
			return nil
		}
		data, err := json.Marshal(event)
		if err != nil {
			os.Exit(80)
		}
		file, err := os.OpenFile(filepath.Join(conf.FileHarbor, "crash-event.json"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			os.Exit(81)
		}
		if _, err = file.Write(data); err != nil {
			os.Exit(82)
		}
		if file.Sync() != nil || file.Close() != nil {
			os.Exit(83)
		}
		os.Exit(73)
		return nil
	})
	var err error
	if kind == "extract" {
		_, err = ExtractArchiveTargetContext(ctx, "source.zip", ExtractionTarget{Mode: "chosen", Directory: "out"})
	} else {
		_, _, info, e := ResolveExisting("source", false)
		if e != nil {
			os.Exit(84)
		}
		err = ExecuteFileJob(ctx, kind, []FileJobSource{{Path: "source", Version: EntryVersion(info)}}, "out", "")
	}
	if err != nil {
		os.Exit(85)
	}
	os.Exit(86)
}

func TestOperationProcessCrashBoundaries(t *testing.T) {
	for _, kind := range []string{"copy", "compress", "extract"} {
		for _, phase := range []string{"staging", "publish_intent", "published", "stage_cleaned"} {
			t.Run(kind+"/"+phase, func(t *testing.T) {
				root := t.TempDir()
				if err := os.Mkdir(filepath.Join(root, "out"), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(root, "source"), []byte("original"), 0600); err != nil {
					t.Fatal(err)
				}
				writeZIPFixture(t, filepath.Join(root, "source.zip"), []zipFixtureEntry{{name: "source", data: []byte("original"), mode: 0600}})
				cmd := exec.Command(os.Args[0], "-test.run=^TestOperationCrashHelper$")
				cmd.Env = append(os.Environ(), "FILEHARBOR_CRASH_HELPER=1", "FILEHARBOR_CRASH_ROOT="+root, "FILEHARBOR_CRASH_KIND="+kind, "FILEHARBOR_CRASH_PHASE="+phase)
				var exit *exec.ExitError
				if err := cmd.Run(); !errors.As(err, &exit) || exit.ExitCode() != 73 {
					t.Fatalf("helper did not reach boundary: %v", err)
				}
				data, err := os.ReadFile(filepath.Join(root, "crash-event.json"))
				if err != nil {
					t.Fatal(err)
				}
				var event OperationEvent
				if json.Unmarshal(data, &event) != nil || event.Phase != phase {
					t.Fatal("missing crash evidence")
				}
				leaf := "source"
				if kind == "compress" {
					leaf += ".zip"
				}
				_, err = os.Stat(filepath.Join(root, "out", leaf))
				published := phase == "published" || phase == "stage_cleaned"
				if published && err != nil || !published && !os.IsNotExist(err) {
					t.Fatalf("unexpected publication at %s: %v", phase, err)
				}
				source, _ := os.ReadFile(filepath.Join(root, "source"))
				if string(source) != "original" {
					t.Fatal("source modified")
				}
			})
		}
	}
}
