package utils

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
)

type FileJobSource struct {
	Path    string `json:"path"`
	Version string `json:"version"`
}

// ExecuteFileJob builds private destination-side outputs before no-replace publication.
func ExecuteFileJob(ctx context.Context, kind string, sources []FileJobSource, destination, name string) error {
	return WithOperationContext(ctx, func() error {
		if (kind != "copy" && kind != "compress") || len(sources) == 0 || len(sources) > MaxListEntries {
			return ErrInvalidPath
		}
		parent, parentRel, parentInfo, err := ResolveDirectory(destination, true)
		if err != nil {
			return err
		}
		for _, source := range sources {
			absolute, relative, expected, err := ResolveExisting(source.Path, false)
			if err != nil {
				return err
			}
			if versionFor(expected) != source.Version {
				return ErrSourceChanged
			}
			if expected.IsDir() && (parentRel == relative || strings.HasPrefix(parentRel, relative+"/")) {
				return operationError("self_descendant")
			}
			leaf := filepath.Base(relative)
			if kind == "compress" {
				leaf += ".zip"
			}
			if name != "" {
				if len(sources) != 1 {
					return ErrInvalidPath
				}
				leaf = name
			}
			if ValidateLeafName(leaf) != nil {
				return ErrInvalidName
			}
			if err := preflightPromotionTargets(parent, []string{leaf}); err != nil {
				return err
			}
			stage, err := os.MkdirTemp(parent, InternalArchiveExtractPrefix)
			if err != nil {
				return operationError("io_error")
			}
			err = func() error {
				stageInfo, err := os.Lstat(stage)
				if err != nil {
					return operationError("io_error")
				}
				defer cleanupOperationStage(ctx, stage, path.Join(parentRel, filepath.Base(stage)), stageInfo)
				if err := observeOperation(ctx, OperationEvent{Phase: "staging", Path: path.Join(parentRel, filepath.Base(stage))}); err != nil {
					return err
				}
				output := filepath.Join(stage, "payload")
				var archive *zip.Writer
				var archiveFile *os.File
				if kind == "compress" {
					archiveFile, err = os.OpenFile(output, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
					if err != nil {
						return operationError("io_error")
					}
					defer archiveFile.Close()
					archive = zip.NewWriter(archiveFile)
					defer archive.Close()
				}
				type identity struct {
					absolute string
					info     os.FileInfo
				}
				identities := []identity{}
				var walk func(string, string, string, int) error
				walk = func(source, target, zipName string, depth int) error {
					if err := contextOperationError(ctx); err != nil {
						return err
					}
					if len(identities) >= 100000 || depth > 128 {
						return ErrBatchLimitExceeded
					}
					info, err := safeEntryInfo(source)
					if err != nil {
						return err
					}
					if depth == 0 && (!os.SameFile(info, expected) || versionFor(info) != versionFor(expected)) {
						return ErrSourceChanged
					}
					identities = append(identities, identity{source, info})
					if info.IsDir() {
						if archive == nil {
							if err := os.Mkdir(target, 0700); err != nil {
								return operationError("io_error")
							}
						} else {
							header, err := zip.FileInfoHeader(info)
							if err != nil {
								return err
							}
							header.Name = zipName + "/"
							if _, err := archive.CreateHeader(header); err != nil {
								return err
							}
						}
						handle, err := os.Open(source)
						if err != nil {
							return operationError("io_error")
						}
						defer handle.Close()
						opened, err := handle.Stat()
						if err != nil || !os.SameFile(opened, info) {
							return ErrSourceChanged
						}
						for {
							entries, readErr := handle.ReadDir(64)
							for _, entry := range entries {
								if ValidateLeafName(entry.Name()) != nil {
									return ErrUnsupportedType
								}
								if err := walk(filepath.Join(source, entry.Name()), filepath.Join(target, entry.Name()), path.Join(zipName, entry.Name()), depth+1); err != nil {
									return err
								}
							}
							if readErr == io.EOF {
								break
							}
							if readErr != nil {
								return operationError("io_error")
							}
						}
						if archive == nil {
							if err := syncOperationDirectory(target); err != nil {
								return operationError("io_error")
							}
						}
						return observeOperation(ctx, OperationEvent{Phase: "writing", Items: 1})
					}
					input, err := os.Open(source)
					if err != nil {
						return operationError("io_error")
					}
					defer input.Close()
					opened, err := input.Stat()
					if err != nil || !os.SameFile(info, opened) || versionFor(info) != versionFor(opened) {
						return ErrSourceChanged
					}
					var writer io.Writer
					var file *os.File
					if archive == nil {
						file, err = os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
						if err != nil {
							return operationError("io_error")
						}
						defer file.Close()
						writer = file
					} else {
						header, err := zip.FileInfoHeader(info)
						if err != nil {
							return err
						}
						header.Name = zipName
						header.Method = zip.Deflate
						writer, err = archive.CreateHeader(header)
						if err != nil {
							return err
						}
					}
					digest := sha256.New()
					buffer := make([]byte, 64<<10)
					var written int64
					for {
						if err := contextOperationError(ctx); err != nil {
							return err
						}
						n, readErr := input.Read(buffer)
						if n > 0 {
							if int64(n) > info.Size()-written {
								return ErrSourceChanged
							}
							count, err := writer.Write(buffer[:n])
							if err != nil || count != n {
								return operationError("io_error")
							}
							digest.Write(buffer[:n])
							written += int64(n)
							if err := observeOperation(ctx, OperationEvent{Phase: "writing", Bytes: int64(n)}); err != nil {
								return err
							}
						}
						if readErr == io.EOF {
							break
						}
						if readErr != nil {
							return operationError("io_error")
						}
					}
					if written != info.Size() {
						return ErrSourceChanged
					}
					if _, err := input.Seek(0, io.SeekStart); err != nil {
						return operationError("io_error")
					}
					verify := sha256.New()
					var verified int64
					for {
						if err := contextOperationError(ctx); err != nil {
							return err
						}
						n, e := input.Read(buffer)
						if n > 0 {
							verified += int64(n)
							if verified > info.Size() {
								return ErrSourceChanged
							}
							verify.Write(buffer[:n])
						}
						if e == io.EOF {
							break
						}
						if e != nil {
							return operationError("io_error")
						}
					}
					if string(digest.Sum(nil)) != string(verify.Sum(nil)) {
						return ErrSourceChanged
					}
					if file != nil {
						if err := file.Sync(); err != nil {
							return operationError("io_error")
						}
						if err := file.Close(); err != nil {
							return operationError("io_error")
						}
					}
					return observeOperation(ctx, OperationEvent{Phase: "writing", Items: 1})
				}
				if err := walk(absolute, output, filepath.Base(relative), 0); err != nil {
					return err
				}
				if archive != nil {
					if err := archive.Close(); err != nil {
						return operationError("io_error")
					}
					if err := archiveFile.Sync(); err != nil {
						return operationError("io_error")
					}
					if err := archiveFile.Close(); err != nil {
						return operationError("io_error")
					}
				}
				if err := observeOperation(ctx, OperationEvent{Phase: "verifying"}); err != nil {
					return err
				}
				for _, item := range identities {
					if err := contextOperationError(ctx); err != nil {
						return err
					}
					current, err := safeEntryInfo(item.absolute)
					if err != nil || !os.SameFile(current, item.info) || versionFor(current) != versionFor(item.info) {
						return ErrSourceChanged
					}
				}
				current, _, info, err := ResolveDirectory(parentRel, true)
				if err != nil || current != parent || !os.SameFile(info, parentInfo) {
					return ErrSourceChanged
				}
				if err := contextOperationError(ctx); err != nil {
					return err
				}
				published := path.Join(parentRel, leaf)
				if err := observeOperation(ctx, OperationEvent{Phase: "publish_intent", Path: published}); err != nil {
					return err
				}
				if err := renameNoReplace(output, filepath.Join(parent, leaf)); err != nil {
					if destinationAlreadyExists(filepath.Join(parent, leaf)) {
						return ErrDestinationExists
					}
					return operationError("io_error")
				}
				if err := syncOperationDirectory(parent); err != nil {
					return operationError("execution_partial")
				}
				if err := observeOperation(ctx, OperationEvent{Phase: "published", Path: published}); err != nil {
					return operationError("execution_partial")
				}
				return nil
			}()
			if err != nil {
				return err
			}
		}
		return nil
	})
}
