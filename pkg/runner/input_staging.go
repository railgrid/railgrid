/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package runner

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/railgrid/railgrid/pkg/runner/harness"
)

// StageInput stores one authenticated file for a future StartRequest. Bytes
// live outside the durable attempt journal until Start consumes the exact
// digest and size listed in its manifest.
func (r *Runner) StageInput(ctx context.Context, attemptID string, ref InputFile, body io.Reader) error {
	if err := validateIdentifier("attemptID", attemptID); err != nil {
		return protocolError(ErrorInvalidRequest, false, err.Error(), nil)
	}
	if err := ValidateInputFile(ref); err != nil {
		return protocolError(ErrorInvalidRequest, false, err.Error(), nil)
	}
	if body == nil {
		return protocolError(ErrorInvalidRequest, false, "input file body is required", nil)
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	dir := stagedInputDir(r.cfg.StateDir, attemptID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return protocolError(ErrorUnavailable, true, "create input staging directory: "+err.Error(), nil)
	}
	tmp, err := os.CreateTemp(dir, ".input-*.tmp")
	if err != nil {
		return protocolError(ErrorUnavailable, true, "create staged input: "+err.Error(), nil)
	}
	tmpPath := tmp.Name()
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
	}()
	hash := sha256.New()
	tracked := &inputWriteTracker{writer: tmp}
	written, err := io.Copy(io.MultiWriter(tracked, hash), io.LimitReader(body, ref.Size+1))
	if err != nil {
		if tracked.err != nil {
			return protocolError(ErrorUnavailable, true, "write staged input: "+tracked.err.Error(), nil)
		}
		return protocolError(ErrorInvalidRequest, false, "read staged input: "+err.Error(), nil)
	}
	if written != ref.Size || "sha256:"+hex.EncodeToString(hash.Sum(nil)) != ref.Digest {
		return protocolError(ErrorInvalidRequest, false, "input bytes do not match the declared size and digest", nil)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return protocolError(ErrorUnavailable, true, "sync staged input: "+err.Error(), nil)
	}
	if err := tmp.Close(); err != nil {
		return protocolError(ErrorUnavailable, true, "close staged input: "+err.Error(), nil)
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return protocolError(ErrorUnavailable, true, "runner is closed", nil)
	}
	if existing := r.state.Attempts[attemptID]; existing != nil {
		if inputRefMatches(existing.Start.InputFiles, ref) && verifyInputFile(materializedInputPath(existing.Receipt.Workdir, attemptID, ref), ref) == nil {
			// A caller retrying after an unknown Start outcome may upload again.
			// The accepted manifest and its materialized file are authoritative.
			return nil
		}
		return protocolError(ErrorIdempotencyConflict, false, "attempt already started with a different input manifest", &existing.Receipt)
	}

	dest := stagedInputPath(r.cfg.StateDir, attemptID, ref.ID)
	if err := verifyInputFile(dest, ref); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		if _, statErr := os.Lstat(dest); statErr == nil {
			return protocolError(ErrorIdempotencyConflict, false, "input ID is already staged with different bytes", nil)
		}
	}

	files, total, err := stagedInputUsage(dir)
	if err != nil {
		return protocolError(ErrorUnavailable, true, "inspect staged inputs: "+err.Error(), nil)
	}
	if files >= MaxInputFiles || total > MaxInputBytes-ref.Size {
		return protocolError(ErrorInvalidRequest, false, "staged inputs exceed the attempt file or byte limit", nil)
	}
	if err := os.Rename(tmpPath, dest); err != nil {
		return protocolError(ErrorUnavailable, true, "commit staged input: "+err.Error(), nil)
	}
	return nil
}

func inputRefMatches(files []InputFile, ref InputFile) bool {
	for _, file := range files {
		if file.ID == ref.ID {
			return file == ref
		}
	}
	return false
}

type inputWriteTracker struct {
	writer io.Writer
	err    error
}

func (w *inputWriteTracker) Write(p []byte) (int, error) {
	n, err := w.writer.Write(p)
	if err != nil {
		w.err = err
	} else if n != len(p) {
		w.err = io.ErrShortWrite
	}
	return n, err
}

// inputOperationError keeps caller mistakes and unsafe workspace paths from
// becoming transient retries, while treating filesystem failures as
// recoverable runner availability problems.
func inputOperationError(operation string, err error) *Error {
	message := operation + ": " + err.Error()
	if isInputFilesystemError(err) {
		return protocolError(ErrorUnavailable, true, message, nil)
	}
	return protocolError(ErrorInvalidRequest, false, message, nil)
}

func isInputFilesystemError(err error) bool {
	var pathErr *os.PathError
	if errors.As(err, &pathErr) {
		return true
	}
	var linkErr *os.LinkError
	if errors.As(err, &linkErr) {
		return true
	}
	var errno syscall.Errno
	return errors.As(err, &errno)
}

func stagedInputUsage(dir string) (int, int64, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return 0, 0, nil
	}
	if err != nil {
		return 0, 0, err
	}
	var count int
	var total int64
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".bin") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return 0, 0, err
		}
		if !info.Mode().IsRegular() {
			return 0, 0, errors.New("staged input directory contains a non-regular file")
		}
		count++
		if total > MaxInputBytes-info.Size() {
			return 0, 0, errors.New("staged input directory exceeds the aggregate limit")
		}
		total += info.Size()
	}
	return count, total, nil
}

func (r *Runner) consumeStagedInputs(workdir, attemptID string, files []InputFile) ([]harness.InputFile, error) {
	if len(files) == 0 {
		return nil, nil
	}
	if err := validateInputFiles(files); err != nil {
		return nil, protocolError(ErrorInvalidRequest, false, err.Error(), nil)
	}
	stagedAvailable := make([]bool, len(files))
	for index, ref := range files {
		staged := stagedInputPath(r.cfg.StateDir, attemptID, ref.ID)
		if err := inspectStagedInput(staged, ref); err == nil {
			stagedAvailable[index] = true
			continue
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, inputOperationError(fmt.Sprintf("staged input %q is invalid", ref.ID), err)
		}
		dirComponents := append([]string{".railgrid", "inputs", attemptID}, inputDirectoryComponents(ref)...)
		dir, err := existingInputDirectory(workdir, dirComponents...)
		if errors.Is(err, os.ErrNotExist) {
			return nil, missingStagedInputError(ref)
		}
		if err != nil {
			return nil, inputOperationError(fmt.Sprintf("inspect materialized input %q", ref.ID), err)
		}
		if err := verifyInputFile(filepath.Join(dir, path.Base(inputRelativePath(ref))), ref); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil, missingStagedInputError(ref)
			}
			return nil, inputOperationError(fmt.Sprintf("verify materialized input %q", ref.ID), err)
		}
	}
	_, err := ensureInputDirectory(workdir, ".railgrid", "inputs", attemptID)
	if err != nil {
		return nil, inputOperationError("prepare input destination", err)
	}
	materialized := make([]harness.InputFile, 0, len(files))
	created := make([]InputFile, 0, len(files))
	rollback := func() {
		for _, ref := range created {
			if err := removeMaterializedInputFile(workdir, attemptID, ref); err != nil {
				log.Printf("input staging: remove partial input %s/%s: %v", attemptID, ref.ID, err)
			}
		}
	}
	for index, ref := range files {
		staged := stagedInputPath(r.cfg.StateDir, attemptID, ref.ID)
		dirComponents := append([]string{".railgrid", "inputs", attemptID}, inputDirectoryComponents(ref)...)
		fileDir, err := ensureInputDirectory(workdir, dirComponents...)
		if err != nil {
			rollback()
			return nil, inputOperationError(fmt.Sprintf("prepare input %q destination", ref.ID), err)
		}
		dest := filepath.Join(fileDir, path.Base(inputRelativePath(ref)))
		if stagedAvailable[index] {
			wasCreated, err := materializeInput(staged, dest, ref)
			if err != nil {
				rollback()
				return nil, inputOperationError(fmt.Sprintf("materialize input %q", ref.ID), err)
			}
			if wasCreated {
				created = append(created, ref)
			}
		}
		materialized = append(materialized, harness.InputFile{
			ID:           ref.ID,
			Name:         ref.Name,
			GroupID:      ref.GroupID,
			RelativePath: ref.RelativePath,
			MediaType:    ref.MediaType,
			Digest:       ref.Digest,
			Size:         ref.Size,
			Path:         materializedInputPath(workdir, attemptID, ref),
		})
	}
	return materialized, nil
}

func missingStagedInputError(ref InputFile) *Error {
	return protocolError(ErrorUnavailable, true, fmt.Sprintf("staged input %q is missing; restage its bytes and retry Start", ref.ID), nil)
}

// inspectStagedInput checks the staged file's type and size without hashing it.
// StageInput already verified the digest; materializeInput rechecks it while
// copying, and launchInputFiles verifies the workspace copy before execution.
func inspectStagedInput(path string, ref InputFile) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("staged input is not a regular file")
	}
	if info.Size() != ref.Size {
		return errors.New("staged input size does not match its manifest")
	}
	return nil
}

func removeMaterializedInputFile(workdir, attemptID string, ref InputFile) error {
	dirComponents := append([]string{".railgrid", "inputs", attemptID}, inputDirectoryComponents(ref)...)
	dir, err := existingInputDirectory(workdir, dirComponents...)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	err = os.Remove(filepath.Join(dir, path.Base(inputRelativePath(ref))))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func removeMaterializedInputs(workdir, attemptID string) error {
	if err := validateIdentifier("attemptID", attemptID); err != nil {
		return err
	}
	root, err := filepath.Abs(workdir)
	if err != nil {
		return err
	}
	rootInfo, err := os.Lstat(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
		return nil
	}
	components := []string{".railgrid", "inputs", attemptID}
	current := root
	for _, component := range components[:len(components)-1] {
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			// The workspace is mutable by the harness. Leave files in place
			// rather than follow a path the runner did not create.
			return nil
		}
	}
	return os.RemoveAll(filepath.Join(current, components[len(components)-1]))
}

// purgeStagedInputs removes old uploads whose coordinator never reached Start.
// It is called at startup and during the regular retention sweep. Accepted
// attempts own their materialized files and are cleaned by attempt retention.
func (r *Runner) purgeStagedInputs(now time.Time) []string {
	root := filepath.Join(r.cfg.StateDir, "input-staging")
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		log.Printf("input staging retention: read directory: %v", err)
		return nil
	}
	retention := time.Duration(r.cfg.AttemptRetentionHours) * time.Hour
	var stale []string
	for _, entry := range entries {
		if !entry.IsDir() || !identifierPattern.MatchString(entry.Name()) {
			continue
		}
		if r.state.Attempts[entry.Name()] != nil {
			continue
		}
		info, err := entry.Info()
		if err != nil || now.Sub(info.ModTime()) <= retention {
			continue
		}
		stale = append(stale, filepath.Join(root, entry.Name()))
	}
	return stale
}

func launchInputFiles(workdir, attemptID string, files []InputFile) ([]harness.InputFile, error) {
	if len(files) == 0 {
		return nil, nil
	}
	if err := validateInputFiles(files); err != nil {
		return nil, err
	}
	inputs := make([]harness.InputFile, 0, len(files))
	for _, ref := range files {
		path := materializedInputPath(workdir, attemptID, ref)
		dirComponents := append([]string{".railgrid", "inputs", attemptID}, inputDirectoryComponents(ref)...)
		if _, err := existingInputDirectory(workdir, dirComponents...); err != nil {
			return nil, fmt.Errorf("materialized input %q is missing or unsafe: %w", ref.ID, err)
		}
		if err := verifyInputFile(path, ref); err != nil {
			return nil, fmt.Errorf("materialized input %q is missing or invalid: %w", ref.ID, err)
		}
		inputs = append(inputs, harness.InputFile{
			ID:           ref.ID,
			Name:         ref.Name,
			GroupID:      ref.GroupID,
			RelativePath: ref.RelativePath,
			MediaType:    ref.MediaType,
			Digest:       ref.Digest,
			Size:         ref.Size,
			Path:         path,
		})
	}
	return inputs, nil
}

func materializeInput(source, dest string, ref InputFile) (bool, error) {
	if err := verifyInputFile(dest, ref); err == nil {
		return false, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		if isInputFilesystemError(err) {
			return false, err
		}
		if _, statErr := os.Lstat(dest); statErr == nil {
			return false, errors.New("input destination already exists with different content")
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return false, statErr
		}
	}
	in, err := openStagedInput(source, ref)
	if err != nil {
		return false, err
	}
	defer func() { _ = in.Close() }()
	tmp, err := os.CreateTemp(filepath.Dir(dest), ".input-*.tmp")
	if err != nil {
		return false, err
	}
	tmpPath := tmp.Name()
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
	}()
	hash := sha256.New()
	written, err := io.Copy(io.MultiWriter(tmp, hash), io.LimitReader(in, ref.Size+1))
	if err != nil {
		return false, err
	}
	if written != ref.Size || "sha256:"+hex.EncodeToString(hash.Sum(nil)) != ref.Digest {
		return false, errors.New("input bytes changed while materializing")
	}
	if err := tmp.Chmod(0o400); err != nil {
		return false, err
	}
	if err := tmp.Sync(); err != nil {
		return false, err
	}
	if err := tmp.Close(); err != nil {
		return false, err
	}
	if err := os.Rename(tmpPath, dest); err != nil {
		return false, err
	}
	return true, nil
}

func openStagedInput(path string, ref InputFile) (*os.File, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() != ref.Size {
		return nil, errors.New("staged input is not the expected regular file")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	opened, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	if !os.SameFile(info, opened) || !opened.Mode().IsRegular() || opened.Size() != ref.Size {
		_ = f.Close()
		return nil, errors.New("staged input changed while it was opened")
	}
	return f, nil
}

func decodeInputFileHeader(value string) (InputFile, error) {
	var ref InputFile
	encoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(encoded) == 0 || len(encoded) > 4096 {
		return ref, errors.New("input metadata header is invalid")
	}
	decoder := json.NewDecoder(strings.NewReader(string(encoded)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&ref); err != nil {
		return InputFile{}, err
	}
	if decoder.Decode(new(any)) != io.EOF {
		return InputFile{}, errors.New("input metadata header contains trailing data")
	}
	if err := ValidateInputFile(ref); err != nil {
		return InputFile{}, err
	}
	return ref, nil
}
