package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/irains/fileharbor/conf"
	"github.com/irains/fileharbor/utils"
)

func TestJobJournalCrashHelper(t *testing.T) {
	if os.Getenv("FILEHARBOR_JOURNAL_HELPER") != "1" {
		return
	}
	root := os.Getenv("FILEHARBOR_JOURNAL_ROOT")
	conf.FileHarbor = root
	phase := os.Getenv("FILEHARBOR_JOURNAL_PHASE")
	_, _, info, err := utils.ResolveExisting("source", false)
	if err != nil {
		os.Exit(81)
	}
	job := &FileJob{Schema: 1, ID: strings.Repeat("a", 32), Scope: strings.Repeat("b", 64), Key: strings.Repeat("c", 32), Kind: "copy", Sources: []utils.FileJobSource{{Path: "source", Version: utils.EntryVersion(info)}}, Destination: "out", State: "running", Phase: "scanning", Created: time.Now(), Updated: time.Now(), Published: []string{}}
	directory := filepath.Join(root, "journal")
	if saveJob(directory, job) != nil {
		os.Exit(82)
	}
	ctx := utils.WithOperationObserver(context.Background(), func(event utils.OperationEvent) error {
		job.Phase = event.Phase
		switch event.Phase {
		case "staging":
			job.Stages = append(job.Stages, event.Path)
		case "publish_intent":
			job.Intent = event.Path
		case "published":
			// The rename has happened, but its result has not reached the journal.
			if phase == "rename_before_record" {
				os.Exit(73)
			}
			job.Published = append(job.Published, event.Path)
			job.Intent = ""
		case "stage_cleaned":
			job.Stages = nil
		}
		if err := saveJob(directory, job); err != nil {
			return err
		}
		if event.Phase == phase {
			os.Exit(73)
		}
		return nil
	})
	if utils.ExecuteFileJob(ctx, "copy", job.Sources, "out", "") != nil {
		os.Exit(83)
	}
	if phase == "before_terminal" {
		os.Exit(73)
	}
	job.State = "succeeded"
	job.Phase = "finished"
	if saveJob(directory, job) != nil {
		os.Exit(84)
	}
	if phase == "after_terminal" {
		os.Exit(73)
	}
	os.Exit(85)
}

func TestJobJournalProcessCrashRecovery(t *testing.T) {
	for _, phase := range []string{"staging", "publish_intent", "rename_before_record", "published", "before_terminal", "after_terminal"} {
		t.Run(phase, func(t *testing.T) {
			root := t.TempDir()
			for _, name := range []string{"out", "journal"} {
				if err := os.Mkdir(filepath.Join(root, name), 0700); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(filepath.Join(root, "source"), []byte("original"), 0600); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(os.Args[0], "-test.run=^TestJobJournalCrashHelper$")
			cmd.Env = append(os.Environ(), "FILEHARBOR_JOURNAL_HELPER=1", "FILEHARBOR_JOURNAL_ROOT="+root, "FILEHARBOR_JOURNAL_PHASE="+phase)
			var exit *exec.ExitError
			if err := cmd.Run(); !errors.As(err, &exit) || exit.ExitCode() != 73 {
				t.Fatalf("boundary not reached: %v", err)
			}
			jobs, err := loadJobs(filepath.Join(root, "journal"))
			if err != nil {
				t.Fatal(err)
			}
			job := jobs[strings.Repeat("a", 32)]
			want := "interrupted"
			if phase == "after_terminal" {
				want = "succeeded"
			}
			if job.State != want {
				t.Fatalf("state=%s", job.State)
			}
			if phase == "rename_before_record" || phase == "publish_intent" {
				if job.Intent != "out/source" {
					t.Fatal("lost uncertain publication")
				}
			}
			if phase == "published" || phase == "before_terminal" || phase == "after_terminal" {
				if len(job.Published) != 1 || job.Intent != "" {
					t.Fatal("lost confirmed publication")
				}
			}
			if phase == "rename_before_record" {
				data, err := os.ReadFile(filepath.Join(root, "out", "source"))
				if err != nil || string(data) != "original" {
					t.Fatal("recovery changed uncertain output")
				}
			}
			if phase == "staging" {
				if len(job.Stages) != 1 {
					t.Fatal("lost staging evidence")
				}
				if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(job.Stages[0]))); err != nil {
					t.Fatal("recovery deleted staging")
				}
			}
		})
	}
}

func TestJobAuditFailureStopsBeforeOutput(t *testing.T) {
	previous := conf.FileHarbor
	conf.FileHarbor = t.TempDir()
	t.Cleanup(func() { conf.FileHarbor = previous })
	os.Mkdir(filepath.Join(conf.FileHarbor, "out"), 0700)
	os.WriteFile(filepath.Join(conf.FileHarbor, "source"), []byte("original"), 0600)
	state := newTestState(t)
	manager, err := newJobManager(state)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.stop()
	_, _, info, _ := utils.ResolveExisting("source", false)
	if err := state.Audit.Close(); err != nil {
		t.Fatal(err)
	}
	job := &FileJob{Kind: "copy", Sources: []utils.FileJobSource{{Path: "source", Version: utils.EntryVersion(info)}}, Destination: "out"}
	if manager.execute(context.Background(), job) == nil || state.Ready() {
		t.Fatal("audit failure allowed work")
	}
	entries, err := os.ReadDir(filepath.Join(conf.FileHarbor, "out"))
	if err != nil || len(entries) != 0 {
		t.Fatal("audit failure wrote output")
	}
}
