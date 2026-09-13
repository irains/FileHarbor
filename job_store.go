package main

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/irains/fileharbor/utils"
)

type FileJob struct {
	Schema          int                    `json:"schema"`
	ID              string                 `json:"id"`
	Owner           string                 `json:"-"`
	Scope           string                 `json:"scope"`
	Key             string                 `json:"key"`
	Previous        string                 `json:"previous,omitempty"`
	Kind            string                 `json:"kind"`
	Sources         []utils.FileJobSource  `json:"sources"`
	Destination     string                 `json:"destination"`
	Name            string                 `json:"name,omitempty"`
	Target          utils.ExtractionTarget `json:"target"`
	State           string                 `json:"state"`
	Phase           string                 `json:"phase"`
	Bytes           int64                  `json:"bytes"`
	Items           int64                  `json:"items"`
	Published       []string               `json:"published"`
	Intent          string                 `json:"intent,omitempty"`
	Stages          []string               `json:"stages,omitempty"`
	Code            string                 `json:"code,omitempty"`
	CancelRequested bool                   `json:"cancel_requested"`
	Created         time.Time              `json:"created"`
	Updated         time.Time              `json:"updated"`
}

func jobTerminal(state string) bool {
	switch state {
	case "succeeded", "failed", "partial", "cancelled", "interrupted":
		return true
	}
	return false
}
func saveJob(directory string, job *FileJob) error {
	data, err := json.Marshal(job)
	if err != nil {
		return err
	}
	if len(data) > 256<<10 {
		return errors.New("job record too large")
	}
	target := filepath.Join(directory, job.ID+".json")
	info, err := os.Lstat(target)
	if err == nil && !info.Mode().IsRegular() {
		return errors.New("invalid job record")
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	file, err := os.CreateTemp(directory, ".job-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err = file.Write(data); err != nil {
		file.Close()
		return err
	}
	if err = file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	if err = protectPrivateFile(file.Name()); err != nil {
		return err
	}
	if err = os.Rename(file.Name(), target); err != nil {
		return err
	}
	return syncRuntimeDirectory(directory)
}
func loadJobs(directory string) (map[string]*FileJob, error) {
	result := map[string]*FileJob{}
	handle, err := os.Open(directory)
	if err != nil {
		return nil, err
	}
	defer handle.Close()
	for {
		entries, readErr := handle.ReadDir(64)
		for _, entry := range entries {
			if !strings.HasSuffix(entry.Name(), ".json") {
				continue
			}
			if len(result) >= 1000 {
				return nil, errors.New("job record quota exceeded")
			}
			id := strings.TrimSuffix(entry.Name(), ".json")
			if !isTrashRecordID(id) {
				return nil, errors.New("invalid job record")
			}
			info, err := entry.Info()
			if err != nil || !info.Mode().IsRegular() || info.Size() > 256<<10 {
				return nil, errors.New("invalid job record")
			}
			file, err := os.Open(filepath.Join(directory, entry.Name()))
			if err != nil {
				return nil, err
			}
			opened, statErr := file.Stat()
			if statErr != nil || !os.SameFile(info, opened) || !opened.Mode().IsRegular() || opened.Size() > 256<<10 {
				file.Close()
				return nil, errors.New("invalid job record")
			}
			decoder := json.NewDecoder(io.LimitReader(file, (256<<10)+1))
			decoder.DisallowUnknownFields()
			var job FileJob
			err = decoder.Decode(&job)
			var trailing any
			if err == nil && decoder.Decode(&trailing) != io.EOF {
				err = errors.New("invalid job record")
			}
			file.Close()
			if err != nil || job.Schema != 1 || job.ID != id || len(job.Scope) != 64 || len(job.Sources) == 0 || len(job.Sources) > utils.MaxListEntries || (!jobTerminal(job.State) && job.State != "queued" && job.State != "running") {
				return nil, errors.New("invalid job record")
			}
			for _, source := range job.Sources {
				clean, err := utils.CleanRelative(source.Path, false)
				if err != nil || clean != source.Path || source.Version == "" {
					return nil, errors.New("invalid job source")
				}
			}
			if err := validateJobRecord(&job); err != nil {
				return nil, err
			}
			if !jobTerminal(job.State) {
				job.State = "interrupted"
				job.Code = "job_interrupted"
				job.Updated = time.Now().UTC()
				if err := saveJob(directory, &job); err != nil {
					return nil, err
				}
			}
			result[id] = &job
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return nil, readErr
		}
	}
	return result, nil
}

func validateJobRecord(job *FileJob) error {
	invalid := errors.New("invalid job record")
	if job.Kind != "copy" && job.Kind != "compress" && job.Kind != "extract" {
		return invalid
	}
	if !isTrashRecordID(job.Key) || job.Bytes < 0 || job.Items < 0 || job.Created.IsZero() || job.Updated.IsZero() {
		return invalid
	}
	if job.Previous != "" && !isTrashRecordID(job.Previous) {
		return invalid
	}
	if _, err := hex.DecodeString(job.Scope); err != nil {
		return invalid
	}
	check := func(value string, root bool) bool {
		clean, err := utils.CleanRelative(value, root)
		return err == nil && clean == value
	}
	if !check(job.Destination, true) || job.Name != "" && utils.ValidateLeafName(job.Name) != nil {
		return invalid
	}
	if len(job.Published) > 10000 || len(job.Stages) > 100 || job.Intent != "" && !check(job.Intent, false) {
		return invalid
	}
	for _, output := range job.Published {
		if !check(output, false) {
			return invalid
		}
	}
	for _, stage := range job.Stages {
		// Stage paths intentionally use a reserved leaf, never accepted as user input.
		if filepath.IsAbs(stage) || strings.Contains(stage, "\\") || strings.Contains(stage, "../") || !strings.HasPrefix(filepath.Base(stage), utils.InternalArchiveExtractPrefix) {
			return invalid
		}
	}
	if job.Kind == "extract" {
		if len(job.Sources) != 1 {
			return invalid
		}
		switch job.Target.Mode {
		case "new_folder":
			if job.Target.Directory != "" || job.Target.Name != "" && utils.ValidateLeafName(job.Target.Name) != nil {
				return invalid
			}
		case "current":
			if job.Target.Directory != "" || job.Target.Name != "" {
				return invalid
			}
		case "chosen":
			if !check(job.Target.Directory, true) || job.Target.Name != "" {
				return invalid
			}
		default:
			return invalid
		}
	}
	return nil
}
