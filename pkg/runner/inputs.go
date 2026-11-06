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
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	// MaxInputFiles and the byte limits bound both the staging area and the
	// workspace copy. A request cannot multiply the per-file limit without
	// bound because the aggregate limit is smaller than that product.
	MaxInputFiles     = 1024
	MaxInputFileBytes = int64(32 << 20)
	MaxInputBytes     = int64(64 << 20)

	inputMetadataHeader = "X-Railgrid-Input-Metadata"
)

// ValidateInputFile checks the stable metadata shared by the coordinator,
// runner and harness adapter. It deliberately accepts only a filename, so a
// caller can never choose a destination directory on the runner.
func ValidateInputFile(file InputFile) error {
	if !identifierPattern.MatchString(file.ID) {
		return errors.New("input file ID is not a valid runner identifier")
	}
	if file.Name == "" || len(file.Name) > 255 || !utf8.ValidString(file.Name) || strings.TrimSpace(file.Name) != file.Name || file.Name == "." || file.Name == ".." || strings.ContainsAny(file.Name, `/\\:`) || strings.HasSuffix(file.Name, ".") {
		return errors.New("input file name must be a single safe filename")
	}
	if file.GroupID != "" && !identifierPattern.MatchString(file.GroupID) {
		return errors.New("input file groupID is not a valid runner identifier")
	}
	if file.RelativePath != "" {
		if err := validateInputRelativePath(file.RelativePath); err != nil {
			return err
		}
		if path.Base(file.RelativePath) != file.Name {
			return errors.New("input file name must match the final relativePath component")
		}
	} else if file.GroupID != "" {
		return errors.New("grouped input files require a relativePath")
	}
	for _, r := range file.Name {
		if unicode.IsControl(r) {
			return errors.New("input file name contains a control character")
		}
	}
	if len(file.MediaType) == 0 || len(file.MediaType) > 127 {
		return errors.New("input file mediaType is required and must be at most 127 bytes")
	}
	mediaType, _, err := mime.ParseMediaType(file.MediaType)
	if err != nil || mediaType == "" {
		return errors.New("input file mediaType must be a valid MIME type")
	}
	if file.Size < 0 || file.Size > MaxInputFileBytes {
		return fmt.Errorf("input file size must be between 0 and %d bytes", MaxInputFileBytes)
	}
	if len(file.Digest) != len("sha256:")+sha256.Size*2 || !strings.HasPrefix(file.Digest, "sha256:") {
		return errors.New("input file digest must be sha256 followed by 64 lowercase hexadecimal characters")
	}
	if _, err := hex.DecodeString(strings.TrimPrefix(file.Digest, "sha256:")); err != nil || strings.ToLower(file.Digest) != file.Digest {
		return errors.New("input file digest must be sha256 followed by 64 lowercase hexadecimal characters")
	}
	return nil
}

func validateInputFiles(files []InputFile) error {
	if len(files) > MaxInputFiles {
		return fmt.Errorf("input file count exceeds the %d-file limit", MaxInputFiles)
	}
	seen := make(map[string]struct{}, len(files))
	destinations := make(map[string]string, len(files))
	destinationChildren := make(map[string]string, len(files))
	var total int64
	for _, file := range files {
		if err := ValidateInputFile(file); err != nil {
			return err
		}
		if _, exists := seen[file.ID]; exists {
			return fmt.Errorf("input file ID %q is repeated", file.ID)
		}
		seen[file.ID] = struct{}{}
		group := file.ID
		if file.GroupID != "" {
			group = "group-" + file.GroupID
		}
		relative := file.RelativePath
		if relative == "" {
			relative = file.Name
		}
		destination := strings.ToLower(group + "/" + relative)
		if previous := collidingInputDestination(destinations, destinationChildren, destination); previous != "" {
			return fmt.Errorf("input destination collision: files %q and %q target the same relative path or a file/directory prefix", previous, file.ID)
		}
		destinations[destination] = file.ID
		for parent := path.Dir(destination); parent != "."; parent = path.Dir(parent) {
			if _, exists := destinationChildren[parent]; !exists {
				destinationChildren[parent] = file.ID
			}
		}
		if total > MaxInputBytes-file.Size {
			return fmt.Errorf("input files exceed the %d-byte aggregate limit", MaxInputBytes)
		}
		total += file.Size
	}
	return nil
}

// collidingInputDestination returns the file already using destination, or a
// file whose destination is a parent or child path. A manifest cannot
// materialize both "assets/logo.png" and "assets/logo.png/metadata.json".
// destinations, destinationChildren and destination are lowercased by the
// caller to preserve the case-insensitive collision behavior used for exact
// paths. The child map lets this check stay proportional to path depth.
func collidingInputDestination(destinations, destinationChildren map[string]string, destination string) string {
	if previous, exists := destinations[destination]; exists {
		return previous
	}
	for parent := path.Dir(destination); parent != "."; parent = path.Dir(parent) {
		if previous, exists := destinations[parent]; exists {
			return previous
		}
	}
	if previous, exists := destinationChildren[destination]; exists {
		return previous
	}
	return ""
}

func validateInputRelativePath(value string) error {
	if value == "" || len(value) > 512 || !utf8.ValidString(value) || strings.TrimSpace(value) != value || path.IsAbs(value) || path.Clean(value) != value || strings.ContainsAny(value, `\\:`) || strings.HasPrefix(value, "/") {
		return errors.New("input relativePath must be a clean relative path")
	}
	for _, component := range strings.Split(value, "/") {
		if component == "" || component == "." || component == ".." || len(component) > 255 {
			return errors.New("input relativePath contains an unsafe component")
		}
		for _, r := range component {
			if unicode.IsControl(r) {
				return errors.New("input relativePath contains a control character")
			}
		}
	}
	decoded, err := url.PathUnescape(value)
	if err != nil {
		return errors.New("input relativePath contains an invalid percent escape")
	}
	if decoded != value && (strings.ContainsAny(decoded, `/\\`) || path.IsAbs(decoded) || path.Clean(decoded) != decoded) {
		return errors.New("input relativePath contains an encoded path separator or traversal")
	}
	if decoded != value {
		for _, component := range strings.Split(decoded, "/") {
			if component == "." || component == ".." {
				return errors.New("input relativePath contains encoded traversal")
			}
		}
	}
	return nil
}

// ValidateInputFiles checks an attempt's complete file manifest before the
// caller stages any bytes or sends Start.
func ValidateInputFiles(files []InputFile) error { return validateInputFiles(files) }

func stagedInputDir(stateDir, attemptID string) string {
	return filepath.Join(stateDir, "input-staging", attemptID)
}

func stagedInputPath(stateDir, attemptID, id string) string {
	return filepath.Join(stagedInputDir(stateDir, attemptID), id+".bin")
}

func materializedInputDir(workdir, attemptID string) string {
	return filepath.Join(workdir, ".railgrid", "inputs", attemptID)
}

func materializedInputPath(workdir, attemptID string, file InputFile) string {
	components := []string{materializedInputDir(workdir, attemptID)}
	components = append(components, inputDirectoryComponents(file)...)
	components = append(components, path.Base(inputRelativePath(file)))
	return filepath.Join(components...)
}

func inputRelativePath(file InputFile) string {
	if file.RelativePath != "" {
		return file.RelativePath
	}
	return file.Name
}

func inputDirectoryComponents(file InputFile) []string {
	group := file.ID
	if file.GroupID != "" {
		group = "group-" + file.GroupID
	}
	components := []string{group}
	if parent := path.Dir(inputRelativePath(file)); parent != "." {
		components = append(components, strings.Split(parent, "/")...)
	}
	return components
}

func verifyInputFile(path string, ref InputFile) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() != ref.Size {
		return errors.New("staged input is not the expected regular file")
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	opened, err := f.Stat()
	if err != nil {
		return err
	}
	if !os.SameFile(info, opened) || !opened.Mode().IsRegular() {
		return errors.New("staged input changed while it was opened")
	}
	hash := sha256.New()
	written, err := io.Copy(io.MultiWriter(io.Discard, hash), io.LimitReader(f, MaxInputFileBytes+1))
	if err != nil {
		return err
	}
	if written != ref.Size || "sha256:"+hex.EncodeToString(hash.Sum(nil)) != ref.Digest {
		return errors.New("staged input size or digest does not match its manifest")
	}
	return nil
}

// ensureInputDirectory creates a path below workdir without following a
// symlink at any component. Workspaces are writable by the harness, so the
// input destination must never trust a preexisting .railgrid symlink.
func ensureInputDirectory(workdir string, components ...string) (string, error) {
	root, err := filepath.Abs(workdir)
	if err != nil {
		return "", err
	}
	rootInfo, err := os.Lstat(root)
	if err != nil {
		return "", err
	}
	if !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("input workdir is not a real directory")
	}
	current := root
	for _, component := range components {
		if component == "" || component == "." || component == ".." || filepath.Base(component) != component {
			return "", errors.New("input destination contains an unsafe path component")
		}
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			if err := os.Mkdir(current, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
				return "", err
			}
			info, err = os.Lstat(current)
		}
		if err != nil {
			return "", err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return "", errors.New("input destination contains a non-directory or symlink")
		}
	}
	return current, nil
}

func existingInputDirectory(workdir string, components ...string) (string, error) {
	root, err := filepath.Abs(workdir)
	if err != nil {
		return "", err
	}
	rootInfo, err := os.Lstat(root)
	if err != nil {
		return "", err
	}
	if !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("input workdir is not a real directory")
	}
	current := root
	for _, component := range components {
		if component == "" || component == "." || component == ".." || filepath.Base(component) != component {
			return "", errors.New("input destination contains an unsafe path component")
		}
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if err != nil {
			return "", err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return "", errors.New("input destination contains a non-directory or symlink")
		}
	}
	return current, nil
}
